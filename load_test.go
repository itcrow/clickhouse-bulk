package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Sustained HTTP ingest against a real listener + mock ClickHouse (dual-write by default).
// Not run in CI by default:
//
//	LOAD_TEST=1 go test -timeout=20m -run TestLoad -count=1 -v .
//
// Optional env: LOAD_TEST_DURATION (default 60s), LOAD_TEST_MAC_COUNT (default 300),
// LOAD_TEST_MAC_INTERVAL (default 1s), LOAD_TEST_RPS (overrides interval), LOAD_TEST_INSERT_INTO, LOAD_TEST_JOURNAL,
// LOAD_TEST_FLUSH_COUNT (default 100), LOAD_TEST_LIVE_ONLY=1, LOAD_TEST_PROGRESS_INTERVAL (default 5s),
// LOAD_TEST_SYNC=1 (block each MAC until HTTP response; RPS drops when latency > interval),
// LOAD_TEST_MAX_INFLIGHT (cap concurrent HTTP requests; 0 = unlimited),
// LOAD_TEST_CH_DELAY (mock ClickHouse response delay for live+backup),
// LOAD_TEST_CH_DELAY_LIVE / LOAD_TEST_CH_DELAY_BACKUP (per-target overrides),
// LOAD_TEST_CH_DELAY_JITTER (extra random delay 0..jitter per CH request),
// LOAD_TEST_PROFILE=1 or LOAD_TEST_PROFILE_DIR (write cpu.prof + heap profiles),
// LOAD_TEST_MEMSTATS=1 (heap/goroutines/cpu in progress logs; on when PROFILE is set),
// LOAD_TEST_CH_DOWN=never|always|window (+ _AFTER, _FOR, _STATUS; _LIVE/_BACKUP per target).
// Payload: SQL INSERT … VALUES (not JSONEachRow).

type loadStatsSnapshot struct {
	ok     int64
	err    int64
	non200 int64
	maxLat time.Duration
}

func (s *loadStats) snapshot() loadStatsSnapshot {
	return loadStatsSnapshot{
		ok:     s.ok.Load(),
		err:    s.err.Load(),
		non200: s.non200.Load(),
		maxLat: time.Duration(s.maxLat.Load()),
	}
}

func (s loadStatsSnapshot) total() int64 {
	return s.ok + s.err + s.non200
}

type loadStats struct {
	sent     atomic.Int64
	ok       atomic.Int64
	err      atomic.Int64
	non200   atomic.Int64
	maxLat   atomic.Int64 // nanoseconds
	inflight atomic.Int64
}

func (s *loadStats) record(status int, lat time.Duration) {
	ns := lat.Nanoseconds()
	for {
		old := s.maxLat.Load()
		if ns <= old || s.maxLat.CompareAndSwap(old, ns) {
			break
		}
	}
	switch {
	case status == http.StatusOK:
		s.ok.Add(1)
	case status == 0:
		s.err.Add(1)
	default:
		s.non200.Add(1)
	}
}

type loadFixture struct {
	bulkURL      string
	dualWrite    bool
	liveReceived *atomic.Int64
	bkpReceived  *atomic.Int64
	sender       Sender
	collect      *Collector
	srv          *Server
	ln           net.Listener
	liveSrv      *httptest.Server
	bkpSrv       *httptest.Server
	testStart    time.Time
}

func startLoadFixture(t *testing.T, journalEnabled bool, flushCount, flushInterval int, dualWrite bool, liveCH, backupCH mockCHOptions) *loadFixture {
	t.Helper()
	testStart := time.Now()
	downTO, connTO := loadTestCHSenderTimeouts(t)

	liveReceived := &atomic.Int64{}
	liveSrv := mockCHServer(liveReceived, liveCH, testStart)

	live := NewClickhouse(downTO, connTO, "", false, 0, 0)
	live.AddServer(liveSrv.URL, false)

	var journal *Journal
	if journalEnabled {
		dir := t.TempDir()
		j, err := NewJournal(dir, false, 0)
		require.NoError(t, err)
		journal = j
	}

	var backup *Clickhouse
	var bkpSrv *httptest.Server
	var bkpReceived *atomic.Int64
	var sender Sender = live
	backupOn := false

	if dualWrite {
		bkpReceived = &atomic.Int64{}
		bkpSrv = mockCHServer(bkpReceived, backupCH, testStart)
		backup = NewClickhouse(downTO, connTO, "", false, 0, 0)
		backup.AddServer(bkpSrv.URL, false)
		sender = NewDualSender(live, backup)
		backupOn = true
	}

	collect := NewCollector(sender, journal, flushCount, flushInterval, 0, true)
	srv := InitServer("", collect, live, nil, backup, nil, backupOn, false, false)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() {
		if err := srv.echo.Server.Serve(ln); err != nil && err != http.ErrServerClosed {
			t.Logf("load server: %v", err)
		}
	}()

	return &loadFixture{
		bulkURL:      "http://" + ln.Addr().String(),
		dualWrite:    dualWrite,
		liveReceived: liveReceived,
		bkpReceived:  bkpReceived,
		sender:       sender,
		collect:      collect,
		srv:          srv,
		ln:           ln,
		liveSrv:      liveSrv,
		bkpSrv:       bkpSrv,
		testStart:    testStart,
	}
}

func (f *loadFixture) close(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	require.NoError(t, f.srv.Shutdown(ctx))
	f.liveSrv.Close()
	if f.bkpSrv != nil {
		f.bkpSrv.Close()
	}
	_ = f.ln.Close()
	if f.collect.Journal != nil {
		_ = f.collect.Journal.Close()
	}
}

func loadTestDualWriteEnabled() bool {
	switch os.Getenv("LOAD_TEST_LIVE_ONLY") {
	case "1", "true", "TRUE":
		return false
	default:
		return true
	}
}

func loadTestConfig(t *testing.T) (duration time.Duration, sensor loadSensorConfig, journal bool, flushCount int, dualWrite bool) {
	t.Helper()
	duration = 60 * time.Second
	if s := os.Getenv("LOAD_TEST_DURATION"); s != "" {
		d, err := time.ParseDuration(s)
		require.NoError(t, err, "LOAD_TEST_DURATION")
		duration = d
	}
	sensor = defaultLoadSensorConfig()
	if s := os.Getenv("LOAD_TEST_MAC_COUNT"); s != "" {
		v, err := strconv.Atoi(s)
		require.NoError(t, err, "LOAD_TEST_MAC_COUNT")
		require.Greater(t, v, 0)
		sensor.MACCount = v
	}
	macIntervalSet := os.Getenv("LOAD_TEST_MAC_INTERVAL") != ""
	if macIntervalSet {
		d, err := time.ParseDuration(os.Getenv("LOAD_TEST_MAC_INTERVAL"))
		require.NoError(t, err, "LOAD_TEST_MAC_INTERVAL")
		require.Greater(t, d, time.Duration(0))
		sensor.Interval = d
	}
	if s := os.Getenv("LOAD_TEST_RPS"); s != "" {
		rps, err := strconv.ParseFloat(s, 64)
		require.NoError(t, err, "LOAD_TEST_RPS")
		require.Greater(t, rps, 0.0)
		applyTargetRPS(&sensor, rps)
		require.False(t, macIntervalSet, "use either LOAD_TEST_RPS or LOAD_TEST_MAC_INTERVAL, not both")
	}
	if s := os.Getenv("LOAD_TEST_INSERT_INTO"); s != "" {
		sensor.InsertInto = s
	}
	journal = os.Getenv("LOAD_TEST_JOURNAL") == "true" || os.Getenv("LOAD_TEST_JOURNAL") == "1"
	flushCount = 100
	if s := os.Getenv("LOAD_TEST_FLUSH_COUNT"); s != "" {
		v, err := strconv.Atoi(s)
		require.NoError(t, err, "LOAD_TEST_FLUSH_COUNT")
		require.Greater(t, v, 0)
		flushCount = v
	}
	dualWrite = loadTestDualWriteEnabled()
	return duration, sensor, journal, flushCount, dualWrite
}

func loadDrainTimeout(t *testing.T) time.Duration {
	t.Helper()
	timeout := 120 * time.Second
	if s := os.Getenv("LOAD_TEST_DRAIN_SEC"); s != "" {
		v, err := strconv.Atoi(s)
		require.NoError(t, err)
		timeout = time.Duration(v) * time.Second
	}
	return timeout
}

func loadTestProgressInterval(t *testing.T) time.Duration {
	t.Helper()
	interval := 5 * time.Second
	if s := os.Getenv("LOAD_TEST_PROGRESS_INTERVAL"); s != "" {
		d, err := time.ParseDuration(s)
		require.NoError(t, err, "LOAD_TEST_PROGRESS_INTERVAL")
		require.Greater(t, d, time.Duration(0))
		interval = d
	}
	return interval
}

func loadProgressBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 100 * float64(width))
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("=", filled) + strings.Repeat("-", width-filled) + "]"
}

func runLoadProgressReporter(ctx context.Context, t *testing.T, totalDuration time.Duration, stats *loadStats, f *loadFixture, interval time.Duration, targetRPS float64, runtimeEnabled bool) {
	start := time.Now()
	prev := loadStatsSnapshot{}
	prevSent := int64(0)
	prevAt := start
	var runtimeWin loadRuntimeWindow
	if runtimeEnabled {
		runtimeWin.sample() // baseline CPU counter
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			elapsed := now.Sub(start)
			pct := float64(elapsed) / float64(totalDuration) * 100
			snap := stats.snapshot()
			windowSec := now.Sub(prevAt).Seconds()
			if windowSec <= 0 {
				windowSec = 1
			}
			delta := snap.total() - prev.total()
			sent := stats.sent.Load()
			sentDelta := sent - prevSent
			rpsWindow := float64(delta) / windowSec
			sentWindow := float64(sentDelta) / windowSec
			rpsTotal := float64(0)
			if elapsed > 0 {
				rpsTotal = float64(snap.total()) / elapsed.Seconds()
			}
			runtimeSample := loadRuntimeSample{}
			if runtimeEnabled {
				runtimeSample = runtimeWin.sample()
			}
			logLoadProgress(t, "load", pct, elapsed, totalDuration, snap, rpsTotal, rpsWindow, sentWindow, targetRPS, sent, stats.inflight.Load(), f, runtimeSample, runtimeEnabled)
			prev = snap
			prevSent = sent
			prevAt = now
		}
	}
}

func waitQueueDrained(t *testing.T, f *loadFixture, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f.collect.FlushAll()
		if f.sender.Empty() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("send queue not drained within %s (pending=%d)", timeout, f.sender.Len())
}

func loadTestSyncMode() bool {
	switch os.Getenv("LOAD_TEST_SYNC") {
	case "1", "true", "TRUE":
		return true
	default:
		return false
	}
}

func loadTestMaxInflight(t *testing.T) int {
	t.Helper()
	if s := os.Getenv("LOAD_TEST_MAX_INFLIGHT"); s != "" {
		v, err := strconv.Atoi(s)
		require.NoError(t, err, "LOAD_TEST_MAX_INFLIGHT")
		require.GreaterOrEqual(t, v, 0)
		return v
	}
	return 0
}

func loadSensorStagger(cfg loadSensorConfig, index int) time.Duration {
	if cfg.MACCount <= 0 {
		return 0
	}
	return time.Duration(int64(index) * int64(cfg.Interval) / int64(cfg.MACCount))
}

func loadTestHTTPTimeout(duration time.Duration) time.Duration {
	if s := os.Getenv("LOAD_TEST_HTTP_TIMEOUT"); s != "" {
		d, err := time.ParseDuration(s)
		if err == nil && d > 0 {
			return d
		}
	}
	if duration*10 > 120*time.Second {
		return duration * 10
	}
	return 120 * time.Second
}

func runLoadSensorClients(ctx context.Context, bulkURL string, cfg loadSensorConfig, stats *loadStats, syncMode bool, maxInflight int, httpTimeout time.Duration) {
	postURL := strings.TrimRight(bulkURL, "/") + "/"
	devices := newLoadSensorFleet(cfg.MACCount)
	client := &http.Client{
		Timeout: httpTimeout,
		Transport: &http.Transport{
			MaxIdleConns:        cfg.MACCount + 8,
			MaxIdleConnsPerHost: cfg.MACCount + 8,
			MaxConnsPerHost:     0,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	var inflightSem chan struct{}
	if maxInflight > 0 {
		inflightSem = make(chan struct{}, maxInflight)
	}

	var postWG sync.WaitGroup
	postOne := func(dev loadSensorDevice, seq int64, nowUnix int64) {
		postWG.Add(1)
		defer postWG.Done()
		if inflightSem != nil {
			defer func() { <-inflightSem }()
		}

		stats.inflight.Add(1)
		defer stats.inflight.Add(-1)

		sql, err := dev.rowSQL(cfg.InsertInto, seq, nowUnix)
		if err != nil {
			stats.record(0, 0)
			return
		}
		start := time.Now()
		resp, err := client.Post(postURL, "text/plain", strings.NewReader(sql))
		lat := time.Since(start)
		if err != nil {
			stats.record(0, lat)
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		stats.record(resp.StatusCode, lat)
		resp.Body.Close()
	}

	var wg sync.WaitGroup
	for i := range devices {
		dev := devices[i]
		stagger := loadSensorStagger(cfg, i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if stagger > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(stagger):
				}
			}
			ticker := time.NewTicker(cfg.Interval)
			defer ticker.Stop()
			var seq int64
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-ticker.C:
					if inflightSem != nil {
						select {
						case inflightSem <- struct{}{}:
						case <-ctx.Done():
							return
						}
					}
					stats.sent.Add(1)
					ts := now.Unix()
					if syncMode {
						postOne(dev, seq, ts)
					} else {
						s := seq
						go postOne(dev, s, ts)
					}
					seq++
				}
			}
		}()
	}
	wg.Wait()
	postWG.Wait()
}

func TestLoad_MockClickHouseReachable(t *testing.T) {
	if os.Getenv("LOAD_TEST") == "" {
		t.Skip("skipped: set LOAD_TEST=1")
	}
	f := startLoadFixture(t, false, 10, 50, false, mockCHOptions{}, mockCHOptions{})
	defer f.close(t)

	dev := loadSensorDevice{mac: "000000000000", phase: 0, ip: "192.168.1.158"}
	sql, err := dev.rowSQL(loadSensorInsertInto, 0, time.Now().Unix())
	require.NoError(t, err)

	_, status, err := f.sender.SendQuery(&ClickhouseRequest{
		Content:  sql,
		Count:    1,
		isInsert: true,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, int64(1), f.liveReceived.Load())
}

func TestLoad_SustainedInsert(t *testing.T) {
	if os.Getenv("LOAD_TEST") == "" {
		t.Skip("skipped: set LOAD_TEST=1 to run sustained load test (see docs/LOAD_TEST.md)")
	}

	duration, sensor, journal, flushCount, dualWrite := loadTestConfig(t)
	liveCH, backupCH := loadTestCHDelays(t)
	_, connTO := loadTestCHSenderTimeouts(t)
	expectLiveCH := loadTestExpectCHTargetReceives(liveCH.Outage, duration)
	expectBackupCH := dualWrite && loadTestExpectCHTargetReceives(backupCH.Outage, duration)
	flushInterval := 50

	f := startLoadFixture(t, journal, flushCount, flushInterval, dualWrite, liveCH, backupCH)
	defer f.close(t)

	expectedRPS := sensor.ExpectedRPS()
	mode := "dual-write"
	if !dualWrite {
		mode = "live-only"
	}
	syncMode := loadTestSyncMode()
	maxInflight := loadTestMaxInflight(t)
	memStats := loadTestMemStatsEnabled()
	prof := startLoadProfile(t)
	t.Logf("load (%s): duration=%s macs=%d interval=%s target_rps=%.0f sync=%v max_inflight=%d ch_delay_live=%s ch_delay_backup=%s ch_outage_live=%s ch_outage_backup=%s expect_live_ch=%v expect_backup_ch=%v ch_connect_timeout=%ds insert=%q journal=%v flush_count=%d profile=%v memstats=%v url=%s",
		mode, duration, sensor.MACCount, sensor.Interval, expectedRPS, syncMode, maxInflight,
		liveCH.delayLog(), backupCH.delayLog(), liveCH.Outage.summary(), backupCH.Outage.summary(),
		expectLiveCH, expectBackupCH, connTO, sensor.InsertInto, journal, flushCount, prof != nil, memStats, f.bulkURL)

	stats := &loadStats{}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	progressCtx, progressCancel := context.WithCancel(context.Background())
	defer progressCancel()
	go runLoadProgressReporter(progressCtx, t, duration, stats, f, loadTestProgressInterval(t), expectedRPS, memStats)

	done := make(chan struct{})
	go func() {
		runLoadSensorClients(ctx, f.bulkURL, sensor, stats, syncMode, maxInflight, loadTestHTTPTimeout(duration))
		close(done)
	}()

	<-done
	progressCancel()

	if prof != nil {
		prof.stopCPU(t)
		prof.writeHeap(t, "heap-load.prof")
	}
	logLoadRuntimeSummary(t, "after-load", loadRuntimeSample{Mem: readLoadMemSnapshot()})

	time.Sleep(500 * time.Millisecond)
	f.collect.FlushAll()

	snap := stats.snapshot()
	total := snap.total()
	require.Greater(t, total, int64(0), "no requests completed")

	errRate := float64(snap.err+snap.non200) / float64(total)
	sent := stats.sent.Load()
	if errRate >= 0.01 {
		if journal && sent > snap.ok {
			t.Logf("WARN: bulk slower than load (journal?): ok=%d sent=%d err=%d — use LOAD_TEST_MAX_INFLIGHT to cap client pressure",
				snap.ok, sent, snap.err)
		}
		require.Less(t, errRate, 0.01, "error rate >= 1%% (ok=%d err=%d non200=%d sent=%d)", snap.ok, snap.err, snap.non200, sent)
	}

	finalRuntime := loadRuntimeSample{Mem: readLoadMemSnapshot()}
	logLoadProgress(t, "final", 100, duration, duration, snap, float64(total)/duration.Seconds(), 0, 0, expectedRPS, stats.sent.Load(), stats.inflight.Load(), f, finalRuntime, memStats)

	waitQueueDrained(t, f, loadDrainTimeout(t))
	require.True(t, f.collect.Empty(), "collector not empty after drain")

	if prof != nil {
		prof.writeHeap(t, "heap-drain.prof")
	}
	logLoadRuntimeSummary(t, "after-drain", loadRuntimeSample{Mem: readLoadMemSnapshot()})

	if expectLiveCH {
		require.Greater(t, f.liveReceived.Load(), int64(0), "live mock ClickHouse received no batches")
	}
	if expectBackupCH {
		require.Greater(t, f.bkpReceived.Load(), int64(0), "backup mock ClickHouse received no batches")
	}
	if dualWrite {
		t.Logf("drain ok: live_batches=%d backup_batches=%d collector_empty=%v ch_outage_live=%s ch_outage_backup=%s",
			f.liveReceived.Load(), f.bkpReceived.Load(), f.collect.Empty(), liveCH.Outage.summary(), backupCH.Outage.summary())
	} else {
		t.Logf("drain ok: live_batches=%d collector_empty=%v ch_outage_live=%s",
			f.liveReceived.Load(), f.collect.Empty(), liveCH.Outage.summary())
	}
}

// TestLoad_SustainedInsert_LiveOnly is an alias for live-only mode (no backup queue).
func TestLoad_SustainedInsert_LiveOnly(t *testing.T) {
	if os.Getenv("LOAD_TEST") == "" {
		t.Skip("skipped: set LOAD_TEST=1")
	}
	if os.Getenv("LOAD_TEST_LIVE_ONLY") == "" {
		t.Setenv("LOAD_TEST_LIVE_ONLY", "1")
	}
	TestLoad_SustainedInsert(t)
}
