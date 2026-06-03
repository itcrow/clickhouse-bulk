package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyTargetRPS(t *testing.T) {
	cfg := defaultLoadSensorConfig()
	cfg.MACCount = 300
	applyTargetRPS(&cfg, 150)
	assert.Equal(t, 150.0, cfg.TargetRPS)
	assert.Equal(t, 2*time.Second, cfg.Interval)
	assert.InDelta(t, 150.0, cfg.ExpectedRPS(), 0.01)
}

func TestExpectedRPS_fromInterval(t *testing.T) {
	cfg := defaultLoadSensorConfig()
	cfg.MACCount = 300
	cfg.Interval = time.Second
	assert.InDelta(t, 300.0, cfg.ExpectedRPS(), 0.01)
}

func TestLoadSensorRow_sqlShape(t *testing.T) {
	dev := loadSensorDevice{mac: "000000000000", phase: 0, ip: "192.168.1.158"}
	sql, err := dev.rowSQL(loadSensorInsertInto, 0, 1780467540)
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(sql, "INSERT INTO air_readings"))
	assert.NotContains(t, strings.ToUpper(sql), "JSONEACHROW")
	assert.NotContains(t, strings.ToUpper(sql), "FORMAT JSON")
	assert.Contains(t, sql, "VALUES (")
	assert.Contains(t, sql, "'000000000000'")
	assert.Contains(t, sql, "1780467540")
	assert.Contains(t, sql, `connection":"wifi"`)
	assert.Contains(t, sql, `ip":"192.168.1.158"`)
}

func TestNewLoadSensorFleet_uniqueMACs(t *testing.T) {
	fleet := newLoadSensorFleet(300)
	require.Len(t, fleet, 300)
	seen := make(map[string]struct{}, 300)
	for _, d := range fleet {
		_, dup := seen[d.mac]
		assert.False(t, dup, "duplicate mac %s", d.mac)
		seen[d.mac] = struct{}{}
	}
}

func TestLoadSensorRow_sqlVaries(t *testing.T) {
	dev := loadSensorDevice{mac: "000000000001", phase: 1.5, ip: "192.168.1.3"}
	s1, err := dev.rowSQL(loadSensorInsertInto, 0, time.Now().Unix())
	require.NoError(t, err)
	s2, err := dev.rowSQL(loadSensorInsertInto, 1, time.Now().Unix()+1)
	require.NoError(t, err)
	assert.NotEqual(t, s1, s2)
}

func TestLoadSensorStagger_spreadsWithinInterval(t *testing.T) {
	cfg := loadSensorConfig{MACCount: 500, Interval: time.Second}
	assert.Equal(t, time.Duration(0), loadSensorStagger(cfg, 0))
	assert.Equal(t, 2*time.Millisecond, loadSensorStagger(cfg, 1))
	assert.Equal(t, time.Second-2*time.Millisecond, loadSensorStagger(cfg, 499))
}

func TestMockCHOptions_sleepDuration(t *testing.T) {
	assert.Equal(t, time.Duration(0), mockCHOptions{}.sleepDuration())
	assert.Equal(t, 50*time.Millisecond, mockCHOptions{Delay: 50 * time.Millisecond}.sleepDuration())

	withJitter := mockCHOptions{Delay: 10 * time.Millisecond, Jitter: 5 * time.Millisecond}
	for i := 0; i < 20; i++ {
		d := withJitter.sleepDuration()
		assert.GreaterOrEqual(t, d, 10*time.Millisecond)
		assert.Less(t, d, 15*time.Millisecond)
	}
}

func TestLoadTestCHDelays_env(t *testing.T) {
	t.Setenv("LOAD_TEST_CH_DELAY", "100ms")
	t.Setenv("LOAD_TEST_CH_DELAY_BACKUP", "250ms")
	t.Setenv("LOAD_TEST_CH_DELAY_JITTER", "10ms")

	live, backup := loadTestCHDelays(t)
	assert.Equal(t, 100*time.Millisecond, live.Delay)
	assert.Equal(t, 10*time.Millisecond, live.Jitter)
	assert.Equal(t, 250*time.Millisecond, backup.Delay)
	assert.Equal(t, 10*time.Millisecond, backup.Jitter)
}

func TestFormatLoadBytes(t *testing.T) {
	assert.Equal(t, "512B", formatLoadBytes(512))
	assert.Equal(t, "1.0KiB", formatLoadBytes(1024))
	assert.Equal(t, "1.5MiB", formatLoadBytes(1024*1024+512*1024))
}

func TestLoadTestProfileEnabled(t *testing.T) {
	t.Setenv("LOAD_TEST_PROFILE", "")
	t.Setenv("LOAD_TEST_PROFILE_DIR", "")
	assert.False(t, loadTestProfileEnabled())

	t.Setenv("LOAD_TEST_PROFILE", "1")
	assert.True(t, loadTestProfileEnabled())
}

func TestLoadTestMemStatsEnabled(t *testing.T) {
	t.Setenv("LOAD_TEST_PROFILE", "")
	t.Setenv("LOAD_TEST_MEMSTATS", "")
	assert.False(t, loadTestMemStatsEnabled())

	t.Setenv("LOAD_TEST_MEMSTATS", "1")
	assert.True(t, loadTestMemStatsEnabled())
}
