package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWantsSyncInsert(t *testing.T) {
	assert.True(t, wantsSyncInsert(true, nil))
	assert.False(t, wantsSyncInsert(false, nil))

	h := http.Header{}
	h.Set(bulkSyncHeader, "1")
	assert.True(t, wantsSyncInsert(false, h))

	h.Set(bulkSyncHeader, "true")
	assert.True(t, wantsSyncInsert(false, h))

	h.Set(bulkSyncHeader, "0")
	assert.False(t, wantsSyncInsert(false, h))
}

func TestBuildSyncBatchedRequest(t *testing.T) {
	params := "database=db&query=" + url.QueryEscape("INSERT INTO t FORMAT TabSeparated")
	req := buildSyncBatchedRequest(params, "1", 7)
	assert.Equal(t, "database=db", req.Params)
	assert.Equal(t, "INSERT INTO t FORMAT TabSeparated", req.Query)
	assert.Equal(t, "INSERT INTO t FORMAT TabSeparated\n1", req.Content)
	assert.Equal(t, []uint64{7}, req.JournalIDs)
	assert.True(t, req.isInsert)
}

func TestServer_SyncBatchedInsert_Success(t *testing.T) {
	var mu sync.Mutex
	var gotBody, gotCT string
	chSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readRequestBody(r.Body, 0)
		mu.Lock()
		gotBody = string(body)
		gotCT = r.Header.Get("Content-Type")
		mu.Unlock()
		w.Header().Set("X-ClickHouse-Summary", `{"written_rows":"1"}`)
		w.WriteHeader(http.StatusOK)
	}))
	defer chSrv.Close()

	sender := NewClickhouse(10, 10, "", false, 0, 0)
	sender.AddServer(chSrv.URL, false)
	c := NewCollector(sender, nil, 1000, 1000, 0, true, false, nil)
	srv := InitServer("", c, sender, nil, nil, nil, false, false, false)

	e := echo.New()
	body := "INSERT INTO db.t FORMAT TabSeparated\n42"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(bulkSyncHeader, "1")
	rec := httptest.NewRecorder()
	require.NoError(t, srv.writeHandler(e.NewContext(req, rec)))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `{"written_rows":"1"}`, rec.Header().Get("X-ClickHouse-Summary"))

	mu.Lock()
	postBody := gotBody
	mu.Unlock()
	assert.Contains(t, postBody, "FORMAT TabSeparated")
	assert.Contains(t, postBody, "42")
	assert.Equal(t, "text/plain", gotCT)
}

func TestServer_SyncBatchedInsert_ClickHouseError(t *testing.T) {
	chSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Code: 60. DB::Exception: Table not found\n"))
	}))
	defer chSrv.Close()

	sender := NewClickhouse(10, 10, "", false, 0, 0)
	sender.AddServer(chSrv.URL, false)
	c := NewCollector(sender, nil, 1000, 1000, 0, true, false, nil)
	srv := InitServer("", c, sender, nil, nil, nil, false, false, false)

	e := echo.New()
	body := "INSERT INTO missing.t FORMAT TabSeparated\n1"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(bulkSyncHeader, "1")
	rec := httptest.NewRecorder()
	require.NoError(t, srv.writeHandler(e.NewContext(req, rec)))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Table not found")
}

func TestServer_SyncOpaqueInsert_Success(t *testing.T) {
	payload := append([]byte("INSERT INTO db.t FORMAT Native\n"), 0xca, 0xfe)
	var mu sync.Mutex
	var gotCT string
	chSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotCT = r.Header.Get("Content-Type")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer chSrv.Close()

	sender := NewClickhouse(10, 10, "", false, 0, 0)
	sender.AddServer(chSrv.URL, false)
	c := NewCollector(sender, nil, 1000, 1000, 0, true, false, nil)
	srv := InitServer("", c, sender, nil, nil, nil, false, false, false)

	e := echo.New()
	q := url.QueryEscape("INSERT INTO db.t FORMAT Native")
	req := httptest.NewRequest(http.MethodPost, "/?query="+q, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(bulkSyncHeader, "1")
	rec := httptest.NewRecorder()
	require.NoError(t, srv.writeHandler(e.NewContext(req, rec)))
	assert.Equal(t, http.StatusOK, rec.Code)

	mu.Lock()
	ct := gotCT
	mu.Unlock()
	assert.Equal(t, "application/octet-stream", ct)
}

func TestServer_SyncDualWrite_BackupAsync(t *testing.T) {
	var mu sync.Mutex
	var liveDone, backupStarted bool
	liveSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		liveDone = true
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer liveSrv.Close()
	backupSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		backupStarted = true
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer backupSrv.Close()

	live := NewClickhouse(10, 10, "", false, 0, 0)
	live.AddServer(liveSrv.URL, false)
	backup := NewClickhouse(10, 10, "", false, 0, 0)
	backup.AddServer(backupSrv.URL, false)
	c := NewCollector(NewDualSender(live, backup), nil, 1000, 1000, 0, true, false, nil)
	srv := InitServer("", c, live, nil, backup, nil, true, false, false)

	e := echo.New()
	body := "INSERT INTO db.t FORMAT TabSeparated\n1"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(bulkSyncHeader, "1")
	rec := httptest.NewRecorder()
	require.NoError(t, srv.writeHandler(e.NewContext(req, rec)))
	assert.Equal(t, http.StatusOK, rec.Code)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return liveDone && backupStarted
	}, 2*time.Second, 10*time.Millisecond)
}

func TestServer_SyncInsert_JournalAck(t *testing.T) {
	dir := t.TempDir()
	journal, err := NewJournal(dir, false, 0)
	require.NoError(t, err)
	defer journal.Close()

	chSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer chSrv.Close()

	sender := NewClickhouse(10, 10, "", false, 0, 0)
	sender.AddServer(chSrv.URL, false)
	sender.Journal = journal
	c := NewCollector(sender, journal, 1000, 1000, 0, true, false, nil)
	srv := InitServer("", c, sender, nil, nil, nil, false, false, false)

	e := echo.New()
	body := "INSERT INTO db.t FORMAT TabSeparated\n9"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(bulkSyncHeader, "1")
	rec := httptest.NewRecorder()
	require.NoError(t, srv.writeHandler(e.NewContext(req, rec)))
	assert.Equal(t, http.StatusOK, rec.Code)

	pending, err := journal.PendingCount()
	require.NoError(t, err)
	assert.Equal(t, 0, pending)
}

func TestReadConfig_SyncInsert(t *testing.T) {
	t.Setenv("SYNC_INSERT", "true")
	cnf, err := ReadConfig("non_existent_config.json")
	require.NoError(t, err)
	assert.True(t, cnf.SyncInsert)
}

func TestServer_AsyncInsert_Unchanged(t *testing.T) {
	chSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("async insert should not hit ClickHouse immediately")
		w.WriteHeader(http.StatusOK)
	}))
	defer chSrv.Close()

	sender := NewClickhouse(10, 10, "", false, 0, 0)
	sender.AddServer(chSrv.URL, false)
	c := NewCollector(sender, nil, 1, 1000, 0, true, false, nil)
	srv := InitServer("", c, sender, nil, nil, nil, false, false, false)

	e := echo.New()
	body := "INSERT INTO db.t FORMAT TabSeparated\n1"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	require.NoError(t, srv.writeHandler(e.NewContext(req, rec)))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Body.String())
}
