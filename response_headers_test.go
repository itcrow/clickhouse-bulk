package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyProxiedResponseHeaders_SkipsHopByHop(t *testing.T) {
	src := http.Header{
		"Connection":              {"keep-alive"},
		"Transfer-Encoding":       {"chunked"},
		"Content-Length":          {"42"},
		"Content-Encoding":        {"gzip"},
		"X-ClickHouse-Query-Id":   {"abc-123"},
		"X-ClickHouse-Summary":    {`{"read_rows":"0"}`},
		"Content-Type":            {"application/json"},
	}
	dst := make(http.Header)
	copyProxiedResponseHeaders(dst, src)

	assert.Equal(t, "abc-123", dst.Get("X-ClickHouse-Query-Id"))
	assert.Equal(t, `{"read_rows":"0"}`, dst.Get("X-ClickHouse-Summary"))
	assert.Equal(t, "application/json", dst.Get("Content-Type"))
	assert.Empty(t, dst.Get("Connection"))
	assert.Empty(t, dst.Get("Transfer-Encoding"))
	assert.Empty(t, dst.Get("Content-Length"))
	assert.Empty(t, dst.Get("Content-Encoding"))
}

func TestClickhouseServer_SendQuery_ReturnsHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-ClickHouse-Query-Id", "qid-test")
		w.Header().Set("Content-Type", "text/tab-separated-values; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("1\n"))
	}))
	defer srv.Close()

	ch := NewClickhouse(10, 10, "", false, 0, 0)
	ch.AddServer(srv.URL, false)

	body, status, headers, err := ch.SendQuery(&ClickhouseRequest{
		Params:   "query=" + url.QueryEscape("SELECT 1"),
		isInsert: false,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "1\n", body)
	assert.Equal(t, "qid-test", headers.Get("X-ClickHouse-Query-Id"))
	assert.Contains(t, headers.Get("Content-Type"), "tab-separated-values")
}

func TestServer_WriteHandler_ProxiedQueryForwardsHeaders(t *testing.T) {
	chSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-ClickHouse-Query-Id", "from-ch")
		w.Header().Set("X-ClickHouse-Summary", `{"read_rows":"1"}`)
		w.Header().Set("Content-Type", "text/tab-separated-values; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("42\n"))
	}))
	defer chSrv.Close()

	sender := NewClickhouse(10, 10, "", false, 0, 0)
	sender.AddServer(chSrv.URL, false)
	c := NewCollector(sender, nil, 1000, 1000, 0, true, false, nil)
	srv := InitServer("", c, nil, nil, nil, nil, false, false, false)

	e := echo.New()
	q := url.QueryEscape("SELECT 1")
	req := httptest.NewRequest(http.MethodPost, "/?query="+q, nil)
	rec := httptest.NewRecorder()
	cctx := e.NewContext(req, rec)

	require.NoError(t, srv.writeHandler(cctx))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "42\n", rec.Body.String())
	assert.Equal(t, "from-ch", rec.Header().Get("X-ClickHouse-Query-Id"))
	assert.Equal(t, `{"read_rows":"1"}`, rec.Header().Get("X-ClickHouse-Summary"))
	assert.Contains(t, rec.Header().Get("Content-Type"), "tab-separated-values")
}
