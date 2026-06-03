package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingSender captures ClickhouseRequest for opaque-path assertions.
type recordingSender struct {
	mu       sync.Mutex
	requests []*ClickhouseRequest
}

func (s *recordingSender) Send(r *ClickhouseRequest) {
	dup := *r
	if len(r.JournalIDs) > 0 {
		dup.JournalIDs = append([]uint64(nil), r.JournalIDs...)
	}
	s.mu.Lock()
	s.requests = append(s.requests, &dup)
	s.mu.Unlock()
}

func (s *recordingSender) SendQuery(r *ClickhouseRequest) (string, int, http.Header, error) {
	return "", http.StatusOK, nil, nil
}

func (s *recordingSender) Len() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.requests))
}

func (s *recordingSender) Empty() bool { return s.Len() == 0 }

func (s *recordingSender) WaitFlush() error { return nil }

func (s *recordingSender) last() *ClickhouseRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return nil
	}
	return s.requests[len(s.requests)-1]
}

func TestShouldOpaqueInsert_Formats(t *testing.T) {
	body := []byte{0x01}
	for _, format := range []string{"Native", "RowBinary", "Parquet", "Arrow", "ArrowStream", "ORC", "Protobuf"} {
		t.Run(format, func(t *testing.T) {
			q := url.QueryEscape("INSERT INTO t FORMAT " + format)
			assert.True(t, shouldOpaqueInsert(false, nil, "", "query="+q, body))
		})
	}
}

func TestShouldOpaqueInsert_Helpers(t *testing.T) {
	assert.False(t, isOctetStreamContentType(""))
	assert.True(t, isOctetStreamContentType("application/octet-stream"))
	assert.True(t, isOctetStreamContentType("Application/OCTET-STREAM; boundary=x"))

	q := "INSERT INTO db.t FORMAT Native"
	assert.Equal(t, q, queryFromParams("database=db&query="+url.QueryEscape(q)))
	assert.Equal(t, q, insertQueryString("query="+url.QueryEscape(q), nil))

	bodyQuery := "INSERT INTO t FORMAT Native\n"
	data := []byte(bodyQuery + "\x01\x02")
	assert.Equal(t, strings.TrimSpace(bodyQuery), insertQueryString("", data))
	assert.True(t, isInsertParamsOrBody("", data))
}

func TestOutboundContentType(t *testing.T) {
	assert.Equal(t, "application/octet-stream", outboundContentType("", "INSERT INTO t FORMAT Native"))
	assert.Equal(t, "application/octet-stream", outboundContentType("", "insert into t format rowbinary"))
	assert.Equal(t, "text/custom", outboundContentType("text/custom; charset=utf-8", "INSERT INTO t FORMAT Native"))
	assert.Equal(t, "text/plain", outboundContentType("", "INSERT INTO t FORMAT TabSeparated"))
}

func TestCollector_PushOpaque(t *testing.T) {
	sender := &recordingSender{}
	c := NewCollector(sender, nil, 1000, 1000, 0, true, false, nil)

	params := "database=db&query=" + url.QueryEscape("INSERT INTO events FORMAT Native")
	payload := string([]byte{0xca, 0xfe})
	c.PushOpaque(params, payload, "application/octet-stream", 42)

	require.Len(t, sender.requests, 1)
	req := sender.requests[0]
	assert.Equal(t, params, req.Params)
	assert.Equal(t, payload, req.Content)
	assert.Equal(t, "application/octet-stream", req.ContentType)
	assert.True(t, req.opaque)
	assert.True(t, req.isInsert)
	assert.Equal(t, 1, req.Count)
	assert.Equal(t, []uint64{42}, req.JournalIDs)
	assert.Contains(t, req.Query, "FORMAT Native")
}

func TestCollector_ReplayJournalRecord_Opaque(t *testing.T) {
	sender2 := &recordingSender{}
	c2 := NewCollector(sender2, nil, 1000, 1000, 0, true, false, nil)
	raw := []byte{0x01, 0x02}
	c2.ReplayJournalRecord(journalRecord{
		ID:          2,
		Params:      "query=" + url.QueryEscape("INSERT INTO t FORMAT Native"),
		Opaque:      true,
		ContentB64:  base64.StdEncoding.EncodeToString(raw),
		ContentType: "application/octet-stream",
	})
	require.Len(t, sender2.requests, 1)
	assert.Equal(t, string(raw), sender2.requests[0].Content)
	assert.Equal(t, "application/octet-stream", sender2.requests[0].ContentType)
}

func TestCollector_ReplayJournalRecord_Batched(t *testing.T) {
	sender := &recordingSender{}
	c := NewCollector(sender, nil, 10, 1000, 0, true, false, nil)
	params := "query=" + url.QueryEscape("INSERT INTO t FORMAT TabSeparated")
	c.ReplayJournalRecord(journalRecord{ID: 1, Params: params, Content: "a\tb"})
	c.ReplayJournalRecord(journalRecord{ID: 2, Params: params, Content: "c\td"})
	assert.Equal(t, int64(0), sender.Len())
	c.FlushAll()
	require.Len(t, sender.requests, 1)
	assert.False(t, sender.requests[0].opaque)
	assert.Contains(t, sender.requests[0].Content, "a\tb")
	assert.Contains(t, sender.requests[0].Content, "c\td")
}

func TestCollector_OpaqueInsertForceAll(t *testing.T) {
	sender := &recordingSender{}
	c := NewCollector(sender, nil, 1000, 1000, 0, true, true, nil)
	params := "query=" + url.QueryEscape("INSERT INTO t FORMAT TabSeparated")
	c.PushOpaque(params, "1\t2\n", "text/plain", 0)
	require.Len(t, sender.requests, 1)
	assert.True(t, sender.requests[0].opaque)
}

func TestJournal_AppendOpaque_WALBase64(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, false, 0)
	require.NoError(t, err)
	defer j.Close()

	payload := string([]byte{0x00, 0xff, 0x42})
	_, err = j.AppendOpaque("query=INSERT+Native", payload, "application/octet-stream")
	require.NoError(t, err)

	f, err := os.Open(j.walPath())
	require.NoError(t, err)
	defer f.Close()
	sc := bufio.NewScanner(f)
	require.True(t, sc.Scan())
	var rec journalRecord
	require.NoError(t, json.Unmarshal(sc.Bytes(), &rec))
	assert.True(t, rec.Opaque)
	assert.Equal(t, "application/octet-stream", rec.ContentType)
	assert.Empty(t, rec.Content)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte(payload)), rec.ContentB64)
	dec, err := base64.StdEncoding.DecodeString(rec.ContentB64)
	require.NoError(t, err)
	assert.Equal(t, payload, string(dec))
}

func TestJournal_AppendOpaqueReplay(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, false, 0)
	require.NoError(t, err)
	defer j.Close()

	payload := string([]byte{0xde, 0xad, 0xbe, 0xef})
	id, err := j.AppendOpaque("query=INSERT+FORMAT+Native", payload, "application/octet-stream")
	require.NoError(t, err)

	sender := &recordingSender{}
	c := NewCollector(sender, j, 1000, 1000, 0, true, false, nil)
	require.NoError(t, j.ReplayUnacked(c.ReplayJournalRecord))

	require.Len(t, sender.requests, 1)
	assert.Equal(t, payload, sender.requests[0].Content)
	assert.Equal(t, uint64(id), sender.requests[0].JournalIDs[0])

	require.NoError(t, j.Ack([]uint64{id}))
}

func newTestEchoServer(collector *Collector) *echo.Echo {
	e := echo.New()
	srv := &Server{Collector: collector}
	e.POST("/", srv.writeHandler)
	return e
}

func newOpaqueTestServer(t *testing.T, collector *Collector, chURL string) *Server {
	t.Helper()
	sender := NewClickhouse(10, 10, "", false, 0, 0)
	sender.AddServer(chURL, false)
	srv := InitServer("", collector, sender, nil, nil, nil, false, false, false)
	srv.echo.POST("/", srv.writeHandler)
	return srv
}

func serveOpaquePOST(e *echo.Echo, path, body, contentType string) (int, string) {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestServer_OpaquePassthrough_Native(t *testing.T) {
	received := make(chan struct {
		contentType string
		params      string
		body        []byte
	}, 1)

	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- struct {
			contentType string
			params      string
			body        []byte
		}{r.Header.Get("Content-Type"), r.URL.RawQuery, body}
		w.WriteHeader(http.StatusOK)
	}))
	defer ch.Close()

	sender := NewClickhouse(10, 10, "", false, 0, 0)
	sender.AddServer(ch.URL, false)
	collector := NewCollector(sender, nil, 1000, 1000, 0, true, false, nil)
	srv := newOpaqueTestServer(t, collector, ch.URL)

	nativeBody := "\x01\x02\x03\x04"
	q := url.QueryEscape("INSERT INTO events FORMAT Native")
	code, resp := serveOpaquePOST(srv.echo, "/?query="+q, nativeBody, "application/octet-stream")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "", resp)

	SafeQuit(collector, sender, 5)
	select {
	case got := <-received:
		assert.Equal(t, "application/octet-stream", got.contentType)
		assert.Equal(t, nativeBody, string(got.body))
		assert.Contains(t, got.params, "query=")
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for ClickHouse mock")
	}
}

func TestServer_OpaquePassthrough_RowBinary(t *testing.T) {
	done := make(chan string, 1)
	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		done <- r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer ch.Close()

	sender := NewClickhouse(10, 10, "", false, 0, 0)
	sender.AddServer(ch.URL, false)
	collector := NewCollector(sender, nil, 1000, 1000, 0, true, false, nil)
	srv := newOpaqueTestServer(t, collector, ch.URL)

	q := url.QueryEscape("INSERT INTO t FORMAT RowBinary")
	code, _ := serveOpaquePOST(srv.echo, "/?query="+q, "\x00\x01", "")
	assert.Equal(t, http.StatusOK, code)
	SafeQuit(collector, sender, 5)

	select {
	case ct := <-done:
		assert.Equal(t, "application/octet-stream", ct)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for RowBinary passthrough")
	}
}

func TestServer_OpaqueInsertConfigForceAll(t *testing.T) {
	sender := &recordingSender{}
	collector := NewCollector(sender, nil, 1000, 1000, 0, true, true, nil)
	e := newTestEchoServer(collector)

	q := url.QueryEscape("INSERT INTO t FORMAT TabSeparated")
	code, resp := serveOpaquePOST(e, "/?query="+q, "1\t2\n", "text/plain")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "", resp)

	// async enqueue — give goroutine a moment
	deadline := time.Now().Add(2 * time.Second)
	for sender.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.NotNil(t, sender.last())
	assert.True(t, sender.last().opaque)
	assert.Equal(t, "1\t2\n", sender.last().Content)
}

func TestServer_acceptOpaqueInsert_EmptyRejected(t *testing.T) {
	sender := &recordingSender{}
	collector := NewCollector(sender, nil, 1000, 1000, 0, true, false, nil)
	srv := &Server{Collector: collector}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := srv.acceptOpaqueInsert(c, "", "", "", false)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "Empty insert\n", rec.Body.String())
	assert.Equal(t, int64(0), sender.Len())
}

func TestServer_OpaqueQueryOnlyEmptyBody(t *testing.T) {
	sender := &recordingSender{}
	collector := NewCollector(sender, nil, 1000, 1000, 0, true, false, nil)
	e := newTestEchoServer(collector)

	// Query in URL, empty body — valid opaque passthrough (driver may send data in query only).
	code, resp := serveOpaquePOST(e, "/?query="+url.QueryEscape("INSERT INTO t FORMAT Native"), "", "application/octet-stream")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "", resp)

	deadline := time.Now().Add(2 * time.Second)
	for sender.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.NotNil(t, sender.last())
	assert.True(t, sender.last().opaque)
	assert.Empty(t, sender.last().Content)
}

func TestServer_OpaqueWithJournal(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, false, 0)
	require.NoError(t, err)
	defer j.Close()

	sender := &recordingSender{}
	collector := NewCollector(sender, j, 1000, 1000, 0, true, false, nil)
	e := newTestEchoServer(collector)

	q := url.QueryEscape("INSERT INTO t FORMAT Native")
	code, _ := serveOpaquePOST(e, "/?query="+q, "\xab\xcd", "application/octet-stream")
	assert.Equal(t, http.StatusOK, code)

	pending, err := j.PendingCount()
	require.NoError(t, err)
	assert.Equal(t, 1, pending)

	deadline := time.Now().Add(2 * time.Second)
	for sender.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.Len(t, sender.requests, 1)
}

func TestServer_OpaqueJournalBacklog(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, false, 1)
	require.NoError(t, err)
	defer j.Close()

	_, err = j.AppendOpaque("query=a", "x", "application/octet-stream")
	require.NoError(t, err)

	sender := &recordingSender{}
	collector := NewCollector(sender, j, 1000, 1000, 0, true, false, nil)
	e := newTestEchoServer(collector)

	code, resp := serveOpaquePOST(e, "/?query="+url.QueryEscape("INSERT INTO t FORMAT Native"), "\x01", "application/octet-stream")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Contains(t, resp, "Journal backlog")
}

func TestServer_BatchedInsertNotOpaque(t *testing.T) {
	sender := &recordingSender{}
	collector := NewCollector(sender, nil, 2, 1000, 0, true, false, nil)
	e := newTestEchoServer(collector)

	q := url.QueryEscape("INSERT INTO table3 (c1, c2, c3) FORMAT TabSeparated")
	path := "/?query=" + q
	// No trailing newline: TabSeparated Add splits on \n; "\n" alone would count as an extra row.
	code1, _ := serveOpaquePOST(e, path, "a\tb\tc", "")
	code2, _ := serveOpaquePOST(e, path, "d\te\tf", "")
	assert.Equal(t, http.StatusOK, code1)
	assert.Equal(t, http.StatusOK, code2)

	deadline := time.Now().Add(2 * time.Second)
	for sender.Len() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	collector.FlushAll()
	sender.WaitFlush()

	require.Len(t, sender.requests, 1)
	assert.False(t, sender.requests[0].opaque)
	assert.Contains(t, sender.requests[0].Content, "a\tb\tc")
	assert.Contains(t, sender.requests[0].Content, "d\te\tf")
}

func TestDualSender_OpaqueSend(t *testing.T) {
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer live.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backup.Close()

	liveCH := NewClickhouse(10, 10, "", false, 0, 0)
	liveCH.AddServer(live.URL, false)
	bkpCH := NewClickhouse(10, 10, "", false, 0, 0)
	bkpCH.AddServer(backup.URL, false)
	dual := NewDualSender(liveCH, bkpCH)

	req := &ClickhouseRequest{
		Params:      "query=INSERT",
		Content:     "\x01\x02",
		ContentType: "application/octet-stream",
		Count:       1,
		isInsert:    true,
		opaque:      true,
	}
	dual.Send(req)

	deadline := time.Now().Add(3 * time.Second)
	for dual.Len() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, int64(0), dual.Len())
}

func TestClickhouse_SendQuery_OpaqueContentType(t *testing.T) {
	var mu sync.Mutex
	var gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotCT = r.Header.Get("Content-Type")
		gotBody = string(body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := NewClickhouse(10, 10, "", false, 0, 0)
	ch.AddServer(srv.URL, false)
	_, status, _, err := ch.SendQuery(&ClickhouseRequest{
		Params:      "query=" + url.QueryEscape("INSERT INTO t FORMAT Native"),
		Content:     "\xff\xfe",
		ContentType: "application/octet-stream",
		isInsert:    true,
		opaque:      true,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)

	mu.Lock()
	ct, body := gotCT, gotBody
	mu.Unlock()
	assert.Equal(t, "application/octet-stream", ct)
	assert.Equal(t, "\xff\xfe", body)
}

func TestReadConfig_OpaqueInsert(t *testing.T) {
	os.Setenv("OPAQUE_INSERT", "true")
	defer os.Unsetenv("OPAQUE_INSERT")

	cnf, err := ReadConfig("non_existent_config.json")
	require.NoError(t, err)
	assert.True(t, cnf.OpaqueInsert)
}

func TestOpaque_BasicAuthAndBodyOnlyInsert(t *testing.T) {
	sender := &recordingSender{}
	collector := NewCollector(sender, nil, 1000, 1000, 0, true, false, nil)
	e := newTestEchoServer(collector)

	body := "INSERT INTO events FORMAT Native\n\x01\x02"
	req := httptest.NewRequest(http.MethodPost, "/?database=db", strings.NewReader(body))
	req.SetBasicAuth("user", "secret")
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	deadline := time.Now().Add(2 * time.Second)
	for sender.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.NotNil(t, sender.last())
	assert.True(t, sender.last().opaque)
	assert.Contains(t, sender.last().Params, "user=user")
	assert.Contains(t, sender.last().Content, "FORMAT Native")
	assert.Contains(t, sender.last().Content, "\x01\x02")
}
