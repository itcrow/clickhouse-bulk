package main

import (
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// mockCHOutage simulates ClickHouse unavailability windows in load tests.
// Env (global or per-target _LIVE / _BACKUP):
//   LOAD_TEST_CH_DOWN=never|always|window
//   LOAD_TEST_CH_DOWN_AFTER, LOAD_TEST_CH_DOWN_FOR
//   LOAD_TEST_CH_DOWN_STATUS=503|502|504
type mockCHOutage struct {
	Kind     string
	After    time.Duration
	Duration time.Duration
	Status   int
}

type mockCHOptions struct {
	Delay  time.Duration
	Jitter time.Duration
	Outage mockCHOutage
}

func (o mockCHOptions) delayLog() string {
	if o.Delay <= 0 && o.Jitter <= 0 {
		return "0"
	}
	if o.Jitter > 0 {
		return fmt.Sprintf("%s+jitter<=%s", o.Delay, o.Jitter)
	}
	return o.Delay.String()
}

func (o mockCHOutage) summary() string {
	if o.Kind == "" || o.Kind == "never" {
		return "never"
	}
	s := o.Kind
	if o.After > 0 {
		s += fmt.Sprintf(" after=%s", o.After)
	}
	if o.Duration > 0 {
		s += fmt.Sprintf(" for=%s", o.Duration)
	}
	if o.Status > 0 {
		s += fmt.Sprintf(" status=%d", o.Status)
	}
	return s
}

func (o mockCHOutage) active(now, testStart time.Time) bool {
	switch strings.ToLower(o.Kind) {
	case "", "never", "0", "false":
		return false
	case "always", "1", "true":
		if o.After > 0 && now.Before(testStart.Add(o.After)) {
			return false
		}
		if o.Duration > 0 {
			start := testStart.Add(o.After)
			return now.Before(start.Add(o.Duration))
		}
		return true
	case "window":
		start := testStart.Add(o.After)
		if now.Before(start) {
			return false
		}
		if o.Duration > 0 && !now.Before(start.Add(o.Duration)) {
			return false
		}
		return true
	default:
		return false
	}
}

func (o mockCHOutage) expectCHReceives(testDuration time.Duration) bool {
	switch strings.ToLower(o.Kind) {
	case "", "never", "0", "false":
		return true
	case "always", "1", "true":
		if o.After > 0 {
			return true
		}
		if o.Duration > 0 && o.Duration < testDuration {
			return true
		}
		return false
	case "window":
		return o.After < testDuration
	default:
		return true
	}
}

func (o mockCHOptions) sleepDuration() time.Duration {
	if o.Delay <= 0 && o.Jitter <= 0 {
		return 0
	}
	d := o.Delay
	if o.Jitter > 0 {
		d += time.Duration(rand.Int63n(int64(o.Jitter)))
	}
	return d
}

func parseMockCHDurationEnv(t *testing.T, name string) (time.Duration, bool) {
	t.Helper()
	s := os.Getenv(name)
	if s == "" {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	require.NoError(t, err, name)
	require.GreaterOrEqual(t, d, time.Duration(0))
	return d, true
}

func parseMockCHOutageEnv(t *testing.T, prefix string) mockCHOutage {
	t.Helper()
	kind := os.Getenv(prefix)
	if kind == "" {
		kind = os.Getenv("LOAD_TEST_CH_DOWN")
	}
	if kind == "" {
		return mockCHOutage{}
	}
	afterKey := prefix + "_AFTER"
	forKey := prefix + "_FOR"
	statusKey := prefix + "_STATUS"
	o := mockCHOutage{Kind: kind, Status: http.StatusServiceUnavailable}
	if d, ok := parseMockCHDurationEnv(t, afterKey); ok {
		o.After = d
	} else if d, ok := parseMockCHDurationEnv(t, "LOAD_TEST_CH_DOWN_AFTER"); ok {
		o.After = d
	}
	if d, ok := parseMockCHDurationEnv(t, forKey); ok {
		o.Duration = d
	} else if d, ok := parseMockCHDurationEnv(t, "LOAD_TEST_CH_DOWN_FOR"); ok {
		o.Duration = d
	}
	status := os.Getenv(statusKey)
	if status == "" {
		status = os.Getenv("LOAD_TEST_CH_DOWN_STATUS")
	}
	if status != "" {
		v, err := strconv.Atoi(status)
		require.NoError(t, err, statusKey)
		require.GreaterOrEqual(t, v, 400)
		o.Status = v
	}
	return o
}

func loadTestCHDelays(t *testing.T) (live, backup mockCHOptions) {
	t.Helper()
	jitter, _ := parseMockCHDurationEnv(t, "LOAD_TEST_CH_DELAY_JITTER")

	if d, ok := parseMockCHDurationEnv(t, "LOAD_TEST_CH_DELAY_LIVE"); ok {
		live.Delay = d
	}
	if d, ok := parseMockCHDurationEnv(t, "LOAD_TEST_CH_DELAY_BACKUP"); ok {
		backup.Delay = d
	}
	if d, ok := parseMockCHDurationEnv(t, "LOAD_TEST_CH_DELAY"); ok {
		if os.Getenv("LOAD_TEST_CH_DELAY_LIVE") == "" {
			live.Delay = d
		}
		if os.Getenv("LOAD_TEST_CH_DELAY_BACKUP") == "" {
			backup.Delay = d
		}
	}
	live.Jitter = jitter
	backup.Jitter = jitter
	live.Outage = parseMockCHOutageEnv(t, "LOAD_TEST_CH_DOWN_LIVE")
	backup.Outage = parseMockCHOutageEnv(t, "LOAD_TEST_CH_DOWN_BACKUP")
	if live.Outage.Kind == "" {
		live.Outage = parseMockCHOutageEnv(t, "LOAD_TEST_CH_DOWN")
	}
	return live, backup
}

func loadTestCHSenderTimeouts(t *testing.T) (downTimeout, connectTimeout int) {
	t.Helper()
	downTimeout = 10
	connectTimeout = 60
	if s := os.Getenv("LOAD_TEST_CH_DOWN_TIMEOUT"); s != "" {
		v, err := strconv.Atoi(s)
		require.NoError(t, err, "LOAD_TEST_CH_DOWN_TIMEOUT")
		require.Greater(t, v, 0)
		downTimeout = v
	}
	if s := os.Getenv("LOAD_TEST_CH_CONNECT_TIMEOUT"); s != "" {
		v, err := strconv.Atoi(s)
		require.NoError(t, err, "LOAD_TEST_CH_CONNECT_TIMEOUT")
		require.Greater(t, v, 0)
		connectTimeout = v
	}
	return downTimeout, connectTimeout
}

func loadTestExpectCHTargetReceives(outage mockCHOutage, testDuration time.Duration) bool {
	switch os.Getenv("LOAD_TEST_EXPECT_CH") {
	case "0":
		return false
	case "1":
		return true
	default:
		return outage.expectCHReceives(testDuration)
	}
}

func mockCHServer(received *atomic.Int64, opts mockCHOptions, testStart time.Time) *httptest.Server {
	status := opts.Outage.Status
	if status == 0 {
		status = http.StatusServiceUnavailable
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if opts.Outage.active(time.Now(), testStart) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("mock clickhouse unavailable\n"))
			return
		}
		if d := opts.sleepDuration(); d > 0 {
			time.Sleep(d)
		}
		received.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("\n"))
	}))
}
