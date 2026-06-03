package main

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetrics_SentAndDumpCounters(t *testing.T) {
	old := backupMetricsEnabled
	t.Cleanup(func() { backupMetricsEnabled = old })

	liveBefore := testutil.ToFloat64(sentCounter)
	incSentCounter("")
	assert.Equal(t, liveBefore+1, testutil.ToFloat64(sentCounter))

	backupMetricsEnabled = false
	bkpBefore := testutil.ToFloat64(sentBkpCounter)
	incSentCounter("backup")
	assert.Equal(t, bkpBefore, testutil.ToFloat64(sentBkpCounter))

	backupMetricsEnabled = true
	incSentCounter("backup")
	assert.Equal(t, bkpBefore+1, testutil.ToFloat64(sentBkpCounter))

	dumpBefore := testutil.ToFloat64(dumpCounter)
	incDumpCounter("")
	assert.Equal(t, dumpBefore+1, testutil.ToFloat64(dumpCounter))
}

func TestMetrics_Gauges(t *testing.T) {
	old := backupMetricsEnabled
	t.Cleanup(func() { backupMetricsEnabled = old })

	setServerGauges("", 2, 1)
	assert.Equal(t, float64(2), testutil.ToFloat64(goodServers))
	assert.Equal(t, float64(1), testutil.ToFloat64(badServers))

	backupMetricsEnabled = true
	setServerGauges("backup", 1, 0)
	assert.Equal(t, float64(1), testutil.ToFloat64(goodBkpServers))

	setSendQueueGauge("", 7)
	assert.Equal(t, float64(7), testutil.ToFloat64(sendQueue))

	setQueuedDumpsGauge("", 3)
	assert.Equal(t, float64(3), testutil.ToFloat64(queuedDumps))

	setDumpDirBytesGauge("", 1024)
	assert.Equal(t, float64(1024), testutil.ToFloat64(dumpDirBytes))
}

func TestMetrics_JournalGauge(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, false, 0)
	require.NoError(t, err)
	defer j.Close()

	_, err = j.Append("p", "row")
	require.NoError(t, err)
	setJournalPendingGauge(j)
	assert.Equal(t, float64(1), testutil.ToFloat64(journalPending))
	assert.Greater(t, testutil.ToFloat64(journalDirBytes), float64(0))
}

func TestMetrics_RecordLastSent(t *testing.T) {
	old := backupMetricsEnabled
	t.Cleanup(func() { backupMetricsEnabled = old })

	recordLastSent("")
	assert.Greater(t, testutil.ToFloat64(lastSentUnix), float64(0))

	backupMetricsEnabled = true
	recordLastSent("backup")
	assert.Greater(t, testutil.ToFloat64(lastBkpSentUnix), float64(0))
}

func TestIsBackupTarget(t *testing.T) {
	assert.True(t, isBackupTarget("backup"))
	assert.False(t, isBackupTarget(""))
}
