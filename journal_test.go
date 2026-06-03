package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJournal_AppendAckReplay(t *testing.T) {
	dir := "journaltest"
	os.RemoveAll(dir)
	defer os.RemoveAll(dir)

	j, err := NewJournal(dir, false, 0)
	assert.Nil(t, err)

	id1, err := j.Append("p1", "row1")
	assert.Nil(t, err)
	id2, err := j.Append("p1", "row2")
	assert.Nil(t, err)

	pending, err := j.PendingCount()
	assert.Nil(t, err)
	assert.Equal(t, 2, pending)

	replayed := 0
	err = j.ReplayUnacked(func(params, content string, journalID uint64) {
		replayed++
		assert.NotZero(t, journalID)
	})
	assert.Nil(t, err)
	assert.Equal(t, 2, replayed)

	err = j.Ack([]uint64{id1})
	assert.Nil(t, err)
	pending, err = j.PendingCount()
	assert.Nil(t, err)
	assert.Equal(t, 1, pending)

	err = j.Ack([]uint64{id2})
	assert.Nil(t, err)
	pending, err = j.PendingCount()
	assert.Nil(t, err)
	assert.Equal(t, 0, pending)

	err = j.Compact()
	assert.Nil(t, err)

	replayed = 0
	err = j.ReplayUnacked(func(params, content string, journalID uint64) {
		replayed++
	})
	assert.Nil(t, err)
	assert.Equal(t, 0, replayed)

	assert.Nil(t, j.Close())
}

func TestJournal_DeferredCompact(t *testing.T) {
	dir := "journaltest-deferred"
	os.RemoveAll(dir)
	defer os.RemoveAll(dir)

	j, err := NewJournal(dir, false, 0)
	assert.Nil(t, err)

	id1, err := j.Append("p1", "row1")
	assert.Nil(t, err)
	_, err = j.Append("p1", "row2")
	assert.Nil(t, err)

	walLines := func() int {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, walFileName))
		assert.Nil(t, err)
		n := 0
		for _, b := range data {
			if b == '\n' {
				n++
			}
		}
		return n
	}

	assert.Equal(t, 2, walLines())

	err = j.Ack([]uint64{id1})
	assert.Nil(t, err)
	assert.Equal(t, 2, walLines(), "single Ack should not compact WAL yet")

	pending, err := j.PendingCount()
	assert.Nil(t, err)
	assert.Equal(t, 1, pending)

	err = j.Compact()
	assert.Nil(t, err)
	assert.Equal(t, 1, walLines())
	assert.Nil(t, j.Close())
}
