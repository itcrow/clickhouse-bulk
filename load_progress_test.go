package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

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

func logLoadProgress(t *testing.T, label string, pct float64, elapsed, total time.Duration, snap loadStatsSnapshot, okRPS, okWindow, sentWindow, targetRPS float64, sent, inflight int64, f *loadFixture, runtime loadRuntimeSample, runtimeEnabled bool) {
	t.Helper()
	if pct > 100 {
		pct = 100
	}
	now := time.Now()
	ts := now.Format(time.RFC3339)
	sentRPS := float64(0)
	if elapsed > 0 {
		sentRPS = float64(sent) / elapsed.Seconds()
	}
	backupBatches := int64(0)
	if f.dualWrite && f.bkpReceived != nil {
		backupBatches = f.bkpReceived.Load()
	}
	maxLatMs := float64(snap.maxLat) / float64(time.Millisecond)

	line := fmt.Sprintf("ts=%s %s %5.1f%% %s elapsed=%s/%s sent=%d ok=%d err=%d non200=%d sent_rps=%.0f sent_window=%.0f ok_rps=%.0f ok_window=%.0f target_rps=%.0f inflight=%d max_lat=%s queue=%d live_batches=%d",
		ts, label, pct, loadProgressBar(pct, 24), elapsed.Truncate(time.Second), total.Truncate(time.Second),
		sent, snap.ok, snap.err, snap.non200, sentRPS, sentWindow, okRPS, okWindow, targetRPS, inflight, snap.maxLat.Truncate(time.Microsecond),
		f.sender.Len(), f.liveReceived.Load())
	if runtimeEnabled {
		line += formatLoadRuntime(runtime)
	}
	if f.dualWrite && f.bkpReceived != nil {
		line += fmt.Sprintf(" backup_batches=%d", backupBatches)
	}
	t.Log(line)

	metrics := fmt.Sprintf(
		"LOAD_PROGRESS ts=%s label=%s elapsed_sec=%.2f pct=%.1f sent=%d ok=%d err=%d non200=%d sent_rps=%.2f ok_rps=%.2f sent_window=%.2f ok_window=%.2f target_rps=%.2f inflight=%d queue=%d live_batches=%d backup_batches=%d max_lat_ms=%.3f",
		ts, label, elapsed.Seconds(), pct, sent, snap.ok, snap.err, snap.non200,
		sentRPS, okRPS, sentWindow, okWindow, targetRPS, inflight, f.sender.Len(), f.liveReceived.Load(), backupBatches, maxLatMs,
	)
	if runtimeEnabled {
		metrics += fmt.Sprintf(" heap_alloc=%d heap_inuse=%d sys=%d goroutines=%d num_gc=%d cpu_cores=%.3f",
			runtime.Mem.HeapAlloc, runtime.Mem.HeapInuse, runtime.Mem.Sys, runtime.Mem.Goroutines, runtime.Mem.NumGC, runtime.CPUCores)
	}
	t.Log(metrics)
}
