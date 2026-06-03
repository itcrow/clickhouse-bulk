package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMockCHOutage_active(t *testing.T) {
	start := time.Now()
	always := mockCHOutage{Kind: "always"}
	assert.True(t, always.active(start.Add(time.Second), start))

	window := mockCHOutage{Kind: "window", After: 10 * time.Second, Duration: 5 * time.Second}
	assert.False(t, window.active(start.Add(5*time.Second), start))
	assert.True(t, window.active(start.Add(12*time.Second), start))
	assert.False(t, window.active(start.Add(20*time.Second), start))
}

func TestMockCHOutage_expectCHReceives(t *testing.T) {
	assert.False(t, mockCHOutage{Kind: "always"}.expectCHReceives(time.Minute))
	assert.True(t, mockCHOutage{Kind: "window", After: 10 * time.Second, Duration: 10 * time.Second}.expectCHReceives(time.Minute))
	assert.True(t, mockCHOutage{Kind: "never"}.expectCHReceives(time.Minute))
}

func TestLoadTestCHDown_env(t *testing.T) {
	t.Setenv("LOAD_TEST_CH_DOWN", "window")
	t.Setenv("LOAD_TEST_CH_DOWN_AFTER", "5s")
	t.Setenv("LOAD_TEST_CH_DOWN_FOR", "10s")
	t.Setenv("LOAD_TEST_CH_DOWN_BACKUP", "always")

	live, backup := loadTestCHDelays(t)
	assert.Equal(t, "window", live.Outage.Kind)
	assert.Equal(t, 5*time.Second, live.Outage.After)
	assert.Equal(t, 10*time.Second, live.Outage.Duration)
	assert.Equal(t, "always", backup.Outage.Kind)
}
