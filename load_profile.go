package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type loadMemSnapshot struct {
	HeapAlloc  uint64
	HeapInuse  uint64
	HeapSys    uint64
	Sys        uint64
	NumGC      uint32
	Goroutines int
}

type loadCPUSnapshot struct {
	TotalCPUSeconds float64
}

type loadRuntimeWindow struct {
	prevCPU loadCPUSnapshot
	prevAt  time.Time
}

type loadRuntimeSample struct {
	Mem      loadMemSnapshot
	CPUCores float64
}

type loadProfiler struct {
	dir     string
	cpuFile *os.File
}

func loadTestProfileEnabled() bool {
	switch os.Getenv("LOAD_TEST_PROFILE") {
	case "1", "true", "TRUE":
		return true
	default:
		return os.Getenv("LOAD_TEST_PROFILE_DIR") != ""
	}
}

func loadTestMemStatsEnabled() bool {
	if loadTestProfileEnabled() {
		return true
	}
	switch os.Getenv("LOAD_TEST_MEMSTATS") {
	case "1", "true", "TRUE":
		return true
	default:
		return false
	}
}

func loadTestProfileDir(t *testing.T) string {
	t.Helper()
	if d := os.Getenv("LOAD_TEST_PROFILE_DIR"); d != "" {
		require.NoError(t, os.MkdirAll(d, 0o755))
		return d
	}
	dir := filepath.Join(os.TempDir(), "clickhouse-bulk-load-"+time.Now().Format("20060102-150405"))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	t.Logf("profile dir: %s", dir)
	return dir
}

func startLoadProfile(t *testing.T) *loadProfiler {
	t.Helper()
	if !loadTestProfileEnabled() {
		return nil
	}
	dir := loadTestProfileDir(t)
	f, err := os.Create(filepath.Join(dir, "cpu.prof"))
	require.NoError(t, err)
	require.NoError(t, pprof.StartCPUProfile(f))
	return &loadProfiler{dir: dir, cpuFile: f}
}

func (p *loadProfiler) stopCPU(t *testing.T) {
	t.Helper()
	if p == nil {
		return
	}
	pprof.StopCPUProfile()
	require.NoError(t, p.cpuFile.Close())
	t.Logf("profile: cpu -> %s/cpu.prof", p.dir)
}

func (p *loadProfiler) writeHeap(t *testing.T, name string) {
	t.Helper()
	if p == nil {
		return
	}
	f, err := os.Create(filepath.Join(p.dir, name))
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, pprof.WriteHeapProfile(f))
	t.Logf("profile: heap -> %s/%s", p.dir, name)
}

func readLoadMemSnapshot() loadMemSnapshot {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return loadMemSnapshot{
		HeapAlloc:  ms.Alloc,
		HeapInuse:  ms.HeapInuse,
		HeapSys:    ms.HeapSys,
		Sys:        ms.Sys,
		NumGC:      ms.NumGC,
		Goroutines: runtime.NumGoroutine(),
	}
}

func readLoadCPUSnapshot() loadCPUSnapshot {
	samples := []metrics.Sample{{Name: "/cpu/classes/total:cpu-seconds"}}
	metrics.Read(samples)
	if samples[0].Value.Kind() == metrics.KindFloat64 {
		return loadCPUSnapshot{TotalCPUSeconds: samples[0].Value.Float64()}
	}
	return loadCPUSnapshot{}
}

func (w *loadRuntimeWindow) sample() loadRuntimeSample {
	now := time.Now()
	mem := readLoadMemSnapshot()
	cpuNow := readLoadCPUSnapshot()
	cpuCores := 0.0
	if !w.prevAt.IsZero() {
		sec := now.Sub(w.prevAt).Seconds()
		if sec > 0 {
			cpuCores = (cpuNow.TotalCPUSeconds - w.prevCPU.TotalCPUSeconds) / sec
		}
	}
	w.prevCPU = cpuNow
	w.prevAt = now
	return loadRuntimeSample{Mem: mem, CPUCores: cpuCores}
}

func formatLoadBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatLoadRuntime(sample loadRuntimeSample) string {
	return fmt.Sprintf(" heap_alloc=%s heap_inuse=%s sys=%s goroutines=%d num_gc=%d cpu_cores=%.2f",
		formatLoadBytes(sample.Mem.HeapAlloc),
		formatLoadBytes(sample.Mem.HeapInuse),
		formatLoadBytes(sample.Mem.Sys),
		sample.Mem.Goroutines,
		sample.Mem.NumGC,
		sample.CPUCores,
	)
}

func logLoadRuntimeSummary(t *testing.T, label string, sample loadRuntimeSample) {
	t.Helper()
	if !loadTestMemStatsEnabled() {
		return
	}
	t.Logf("%s runtime:%s", label, formatLoadRuntime(sample))
}
