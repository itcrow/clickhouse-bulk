package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newOpsServer(t *testing.T, backupOn bool) (*Server, *Clickhouse, *Clickhouse, *FileDumper, *FileDumper) {
	t.Helper()
	live := NewClickhouse(10, 10, "", false, 0, 0)
	live.AddServer("http://live-ch:8123", false)
	liveDumper := NewDumper(t.TempDir())

	var backup *Clickhouse
	var backupDumper *FileDumper
	if backupOn {
		backup = NewClickhouse(10, 10, "", false, 0, 0)
		backup.AddServer("http://backup-ch:8123", false)
		backupDumper = NewDumper(t.TempDir())
	}

	collector := NewCollector(live, nil, 1000, 1000, 0, true, false, nil)
	srv := InitServer("", collector, live, liveDumper, backup, backupDumper, backupOn, false, false)
	return srv, live, backup, liveDumper, backupDumper
}

func jsonRequest(t *testing.T, e *echo.Echo, method, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func TestBuildTargetStatus(t *testing.T) {
	disabled := buildTargetStatus(nil, false)
	assert.False(t, disabled.Enabled)

	ch := NewClickhouse(10, 10, "", false, 0, 0)
	ch.AddServer("http://127.0.0.1:8123", false)
	ch.Send(&ClickhouseRequest{Params: "p", Content: "c", isInsert: true})

	st := buildTargetStatus(ch, true)
	assert.True(t, st.Enabled)
	assert.Equal(t, int64(1), st.QueueLen)
	require.Len(t, st.Servers, 1)
	assert.Equal(t, "http://127.0.0.1:8123", st.Servers[0].URL)
	assert.False(t, st.Servers[0].Bad)

	ch.WaitFlush()
}

func TestStatusHandler_LiveOnly(t *testing.T) {
	block := make(chan struct{})
	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	defer ch.Close()

	srv, live, _, _, _ := newOpsServer(t, false)
	live.Servers[0].URL = ch.URL
	live.Servers[0].Client = ch.Client()

	live.Send(&ClickhouseRequest{Params: "p1", Content: "c1", isInsert: true})
	live.Send(&ClickhouseRequest{Params: "p2", Content: "c2", isInsert: true})

	code, body := jsonRequest(t, srv.echo, http.MethodGet, "/status")
	require.Equal(t, http.StatusOK, code)

	var st FullStatus
	require.NoError(t, json.Unmarshal(body, &st))
	assert.Equal(t, "ok", st.Status)
	assert.True(t, st.Live.Enabled)
	assert.GreaterOrEqual(t, st.Live.QueueLen, int64(1))
	assert.False(t, st.Backup.Enabled)
	assert.Equal(t, ch.URL, st.Live.Servers[0].URL)

	close(block)
	live.WaitFlush()
}

func TestStatusHandler_DualWrite(t *testing.T) {
	block := make(chan struct{})
	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	defer ch.Close()

	srv, _, backup, _, _ := newOpsServer(t, true)
	backup.Servers[0].URL = ch.URL
	backup.Servers[0].Client = ch.Client()

	backup.Send(&ClickhouseRequest{Params: "p1", Content: "c1", isInsert: true})
	backup.Send(&ClickhouseRequest{Params: "p2", Content: "c2", isInsert: true})

	code, body := jsonRequest(t, srv.echo, http.MethodGet, "/status")
	require.Equal(t, http.StatusOK, code)

	var st FullStatus
	require.NoError(t, json.Unmarshal(body, &st))
	assert.True(t, st.Live.Enabled)
	assert.True(t, st.Backup.Enabled)
	assert.GreaterOrEqual(t, st.Backup.QueueLen, int64(1))
	assert.Equal(t, ch.URL, st.Backup.Servers[0].URL)

	close(block)
	backup.WaitFlush()
}

func TestReplayFailedHandler_Live(t *testing.T) {
	srv, live, _, liveDumper, _ := newOpsServer(t, false)

	liveDumper.DumpPrefix = "20990101120000"
	liveDumper.DumpNum = 0
	require.NoError(t, liveDumper.Dump("p=1", "insert into t values (1)", "bad request", dumpKindClientError, 400))
	name := liveDumper.dumpName(1, dumpKindClientError, 400)
	require.NoError(t, liveDumper.moveToFailed(name))

	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ch.Close()
	live.Servers[0].URL = ch.URL

	code, body := jsonRequest(t, srv.echo, http.MethodPost, "/debug/replay-failed?limit=1")
	require.Equal(t, http.StatusOK, code)

	var resp ReplayFailedResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	require.NotNil(t, resp.Live)
	assert.Equal(t, 1, resp.Live.Sent)
	assert.Equal(t, 0, resp.Live.Remaining)
}

func TestReplayFailedHandler_BackupDisabled(t *testing.T) {
	srv, _, _, _, _ := newOpsServer(t, false)
	code, body := jsonRequest(t, srv.echo, http.MethodGet, "/debug/replay-failed?target=backup")
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, string(body), "backup not enabled")
}

func TestReplayFailedHandler_InvalidLimit(t *testing.T) {
	srv, _, _, _, _ := newOpsServer(t, false)
	code, body := jsonRequest(t, srv.echo, http.MethodGet, "/debug/replay-failed?limit=bad")
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, string(body), "invalid limit")
}

func TestReplayFailedHandler_AllTargets(t *testing.T) {
	srv, live, backup, liveDumper, backupDumper := newOpsServer(t, true)

	for _, d := range []*FileDumper{liveDumper, backupDumper} {
		d.DumpPrefix = "20990101120000"
		d.DumpNum = 0
		require.NoError(t, d.Dump("p=1", "row", "err", dumpKindClientError, 400))
		require.NoError(t, d.moveToFailed(d.dumpName(1, dumpKindClientError, 400)))
	}

	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()
	live.AddServer(okSrv.URL, false)
	backup.AddServer(okSrv.URL, false)

	code, body := jsonRequest(t, srv.echo, http.MethodPost, "/debug/replay-failed?target=all")
	require.Equal(t, http.StatusOK, code)

	var resp ReplayFailedResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	require.NotNil(t, resp.Live)
	require.NotNil(t, resp.Backup)
	assert.Equal(t, 1, resp.Live.Sent)
	assert.Equal(t, 1, resp.Backup.Sent)
}

func TestTablesCleanHandler(t *testing.T) {
	srv, _, _, _, _ := newOpsServer(t, false)
	srv.Collector.AddTable("empty-table")
	srv.Collector.mu.Lock()
	for _, tbl := range srv.Collector.Tables {
		tbl.Flush()
	}
	srv.Collector.mu.Unlock()
	require.NotEmpty(t, srv.Collector.Tables)

	code, body := jsonRequest(t, srv.echo, http.MethodGet, "/debug/tables-clean")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, string(body), "cleaned")

	srv.Collector.mu.RLock()
	n := len(srv.Collector.Tables)
	srv.Collector.mu.RUnlock()
	assert.Equal(t, 0, n)
}

func TestWriteHandler_BatchedJournalBacklog(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, false, 1)
	require.NoError(t, err)
	defer j.Close()

	_, err = j.Append("p", "row")
	require.NoError(t, err)

	sender := &recordingSender{}
	collector := NewCollector(sender, j, 1000, 1000, 0, true, false, nil)
	e := newTestEchoServer(collector)

	q := url.QueryEscape("INSERT INTO t FORMAT TabSeparated")
	code, resp := serveOpaquePOST(e, "/?query="+q, "a\tb", "text/plain")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Contains(t, resp, "Journal backlog")
}

func TestWriteHandler_BatchedJournalAppendFail(t *testing.T) {
	j, err := NewJournal(t.TempDir(), false, 0)
	require.NoError(t, err)
	require.NoError(t, j.wal.Close())

	sender := &recordingSender{}
	collector := NewCollector(sender, j, 1000, 1000, 0, true, false, nil)
	e := newTestEchoServer(collector)

	q := url.QueryEscape("INSERT INTO t FORMAT TabSeparated")
	code, resp := serveOpaquePOST(e, "/?query="+q, "a\tb", "text/plain")
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.Contains(t, resp, "Journal write failed")
}

type stallSender struct {
	fakeSender
}

func (s *stallSender) Len() int64  { return 1 }
func (s *stallSender) Empty() bool { return false }

func TestSafeQuit_DrainTimeout(t *testing.T) {
	stall := &stallSender{}
	collect := NewCollector(stall, nil, 1000, 1000, 0, true, false, nil)

	start := time.Now()
	SafeQuit(collect, stall, 1)
	assert.GreaterOrEqual(t, time.Since(start), time.Second)
}

func TestBackupDumpCheckInterval(t *testing.T) {
	assert.Equal(t, 60, backupDumpCheckInterval(Config{BkpDumpCheckInterval: 60, DumpCheckInterval: 10}))
	assert.Equal(t, 10, backupDumpCheckInterval(Config{BkpDumpCheckInterval: 0, DumpCheckInterval: 10}))
}

func TestDualSender_SendQuery_LiveOnly(t *testing.T) {
	var liveHits, backupHits int
	liveSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		liveHits++
		io.ReadAll(r.Body)
		w.Write([]byte("1"))
	}))
	defer liveSrv.Close()
	backupSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupHits++
		w.Write([]byte("1"))
	}))
	defer backupSrv.Close()

	live := NewClickhouse(10, 10, "", false, 0, 0)
	live.AddServer(liveSrv.URL, false)
	backup := NewClickhouse(10, 10, "", false, 0, 0)
	backup.AddServer(backupSrv.URL, false)
	dual := NewDualSender(live, backup)

	resp, status, _, err := dual.SendQuery(&ClickhouseRequest{
		Params:   "query=" + url.QueryEscape("SELECT 1"),
		isInsert: false,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "1", strings.TrimSpace(resp))
	assert.Equal(t, 1, liveHits)
	assert.Equal(t, 0, backupHits)
}

func TestDualSender_WaitFlushAndEmpty(t *testing.T) {
	block := make(chan struct{})
	var inflight int32
	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&inflight, 1)
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	defer ch.Close()

	client := ch.Client()
	live := NewClickhouse(10, 10, "", false, 0, 0)
	live.AddServer(ch.URL, false)
	live.Servers[0].Client = client
	backup := NewClickhouse(10, 10, "", false, 0, 0)
	backup.AddServer(ch.URL, false)
	backup.Servers[0].Client = client
	dual := NewDualSender(live, backup)

	assert.True(t, dual.Empty())
	dual.Send(&ClickhouseRequest{Params: "p", Content: "c", isInsert: true})

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&inflight) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, int32(2), atomic.LoadInt32(&inflight), "live and backup should both be in flight")

	done := make(chan struct{})
	go func() {
		require.NoError(t, dual.WaitFlush())
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("WaitFlush returned before HTTP unblock")
	case <-time.After(200 * time.Millisecond):
	}

	close(block)
	<-done
	assert.True(t, dual.Empty())
	assert.Equal(t, int64(0), dual.Len())
}
