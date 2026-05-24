package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClickhouse_Run_AckJournalOnSuccess(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, false, 0)
	require.NoError(t, err)
	defer j.Close()

	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ch.Close()

	sender := NewClickhouse(10, 10, "", false, 0, 0)
	sender.AddServer(ch.URL, false)
	sender.Journal = j

	id, err := j.Append("params=1", "row")
	require.NoError(t, err)

	sender.Send(&ClickhouseRequest{
		Params:     "params=1",
		Content:    "row",
		isInsert:   true,
		JournalIDs: []uint64{id},
	})
	require.NoError(t, sender.WaitFlush())

	pending, err := j.PendingCount()
	require.NoError(t, err)
	assert.Equal(t, 0, pending)
}

func TestClickhouse_Run_AckJournalAfterDump(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, false, 0)
	require.NoError(t, err)
	defer j.Close()

	dumpDir := t.TempDir()
	dumper := NewDumper(dumpDir)

	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad"))
	}))
	defer ch.Close()

	sender := NewClickhouse(10, 10, "", false, 0, 0)
	sender.AddServer(ch.URL, false)
	sender.Journal = j
	sender.Dumper = dumper

	id, err := j.Append("params=1", "row")
	require.NoError(t, err)

	sender.Send(&ClickhouseRequest{
		Params:     "params=1",
		Content:    "row",
		isInsert:   true,
		JournalIDs: []uint64{id},
	})
	require.NoError(t, sender.WaitFlush())

	pending, err := j.PendingCount()
	require.NoError(t, err)
	assert.Equal(t, 0, pending)

	files, err := dumper.listPendingDumpFiles()
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Contains(t, files[0], dumpKindClientError)

	require.NoError(t, dumper.ProcessNextDump(sender))
	failed, err := dumper.listFailedDumpFiles()
	require.NoError(t, err)
	assert.Len(t, failed, 1)
}

func TestClickhouse_SendQuery_MergeQueryParams(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := NewClickhouse(10, 10, "", false, 0, 0)
	ch.QueryParams = "database=standby"
	ch.AddServer(srv.URL, false)

	_, status, _, err := ch.SendQuery(&ClickhouseRequest{Params: "query=SELECT+1", isInsert: false})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, gotQuery, "database=standby")
	assert.Contains(t, gotQuery, "query=SELECT")
}

func TestClickhouse_ServersSnapshot(t *testing.T) {
	ch := NewClickhouse(10, 10, "", false, 0, 0)
	ch.AddServer("http://a:8123", false)
	ch.Servers[0].Bad = true

	snap := ch.ServersSnapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "http://a:8123", snap[0].URL)
	assert.True(t, snap[0].Bad)
}

func TestClickhouse_Run_ClientErrorDumpPrefix(t *testing.T) {
	dumpDir := t.TempDir()
	dumper := NewDumper(dumpDir)

	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad"))
	}))
	defer ch.Close()

	sender := NewClickhouse(10, 10, "", false, 0, 0)
	sender.AddServer(ch.URL, false)
	sender.Dumper = dumper

	sender.Send(&ClickhouseRequest{Params: "p", Content: "c", isInsert: true})
	require.NoError(t, sender.WaitFlush())

	files, err := dumper.listPendingDumpFiles()
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Contains(t, files[0], dumpKindClientError)
}

func TestClickhouse_Run_RetriesSecondServer(t *testing.T) {
	var hits []string
	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, "bad")
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer s1.Close()
	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, "good")
		w.WriteHeader(http.StatusOK)
	}))
	defer s2.Close()

	sender := NewClickhouse(1, 10, "", false, 0, 0)
	sender.AddServer(s1.URL, false)
	sender.AddServer(s2.URL, false)

	sender.Send(&ClickhouseRequest{Params: "p", Content: "c", isInsert: true})
	require.NoError(t, sender.WaitFlush())

	assert.Contains(t, hits, "bad")
	assert.Contains(t, hits, "good")
}

func TestClickhouse_SendWithRateLimit(t *testing.T) {
	t.Skip("rate limit timing is environment-sensitive; covered by rate_limiter_test")
}
