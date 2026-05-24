package main

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJournal_FsyncAndReloadAcked(t *testing.T) {
	dir := t.TempDir()

	j1, err := NewJournal(dir, true, 0)
	require.NoError(t, err)
	_, err = j1.Append("p", "row")
	require.NoError(t, err)
	require.NoError(t, j1.Close())

	j2, err := NewJournal(dir, false, 0)
	require.NoError(t, err)
	defer j2.Close()

	pending, err := j2.PendingCount()
	require.NoError(t, err)
	assert.Equal(t, 1, pending)

	replayed := 0
	require.NoError(t, j2.ReplayUnacked(func(rec journalRecord) { replayed++ }))
	assert.Equal(t, 1, replayed)
}

func TestJournal_CompactKeepsPendingOnly(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, false, 0)
	require.NoError(t, err)
	defer j.Close()

	id1, err := j.Append("p1", "a")
	require.NoError(t, err)
	id2, err := j.Append("p2", "b")
	require.NoError(t, err)
	require.NoError(t, j.Ack([]uint64{id1}))

	require.NoError(t, j.Compact())

	pending, err := j.PendingCount()
	require.NoError(t, err)
	assert.Equal(t, 1, pending)

	f, err := os.Open(j.walPath())
	require.NoError(t, err)
	defer f.Close()
	lines := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec journalRecord
		require.NoError(t, json.Unmarshal(sc.Bytes(), &rec))
		assert.Equal(t, id2, rec.ID)
		lines++
	}
	assert.Equal(t, 1, lines)
}

func TestJournal_ReplayUnacked_SkipsCorruptLine(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, false, 0)
	require.NoError(t, err)

	id, err := j.Append("good", "row")
	require.NoError(t, err)
	require.NoError(t, j.wal.Close())

	f, err := os.OpenFile(j.walPath(), os.O_APPEND|os.O_WRONLY, 0644)
	require.NoError(t, err)
	_, err = f.WriteString("not-json\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	j2, err := NewJournal(dir, false, 0)
	require.NoError(t, err)
	defer j2.Close()

	replayed := 0
	require.NoError(t, j2.ReplayUnacked(func(rec journalRecord) {
		replayed++
		assert.Equal(t, id, rec.ID)
	}))
	assert.Equal(t, 1, replayed)
}

func TestJournal_DirBytes(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, false, 0)
	require.NoError(t, err)
	defer j.Close()

	_, err = j.Append("p", "row")
	require.NoError(t, err)

	n, err := j.DirBytes()
	require.NoError(t, err)
	assert.Greater(t, n, int64(0))
}
