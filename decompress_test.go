package main

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/ch-go/compress"
	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/zstd"
	"github.com/labstack/echo/v4"
	"github.com/pierrec/lz4/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func compressNativeLZ4(t *testing.T, plain []byte) []byte {
	t.Helper()
	w := compress.NewWriter(compress.LevelZero, compress.LZ4)
	require.NoError(t, w.Compress(plain))
	return append([]byte(nil), w.Data...)
}

func compressGzip(t *testing.T, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, err := gw.Write(plain)
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	return buf.Bytes()
}

func compressDeflate(t *testing.T, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	require.NoError(t, err)
	_, err = fw.Write(plain)
	require.NoError(t, err)
	require.NoError(t, fw.Close())
	return buf.Bytes()
}

func compressLZ4Frame(t *testing.T, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := lz4.NewWriter(&buf)
	_, err := w.Write(plain)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func compressZstd(t *testing.T, plain []byte) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	return enc.EncodeAll(plain, nil)
}

func TestDecompressRequestBody_Gzip(t *testing.T) {
	plain := []byte("INSERT INTO t VALUES (1)\n1\n")
	body := compressGzip(t, plain)
	out, qs, err := decompressRequestBody(body, "gzip", "database=db", 0)
	require.NoError(t, err)
	assert.Equal(t, plain, out)
	assert.Equal(t, "database=db", qs)
}

func TestDecompressRequestBody_Deflate(t *testing.T) {
	plain := []byte("hello")
	body := compressDeflate(t, plain)
	out, _, err := decompressRequestBody(body, "deflate", "", 0)
	require.NoError(t, err)
	assert.Equal(t, plain, out)
}

func TestDecompressRequestBody_LZ4Frame(t *testing.T) {
	plain := []byte("INSERT INTO t FORMAT TabSeparated\n1\n")
	body := compressLZ4Frame(t, plain)
	out, _, err := decompressRequestBody(body, "lz4", "", 0)
	require.NoError(t, err)
	assert.Equal(t, plain, out)
}

func TestDecompressRequestBody_ZstdHTTP(t *testing.T) {
	plain := []byte("data")
	body := compressZstd(t, plain)
	out, _, err := decompressRequestBody(body, "zstd", "", 0)
	require.NoError(t, err)
	assert.Equal(t, plain, out)
}

func TestDecompressRequestBody_NativeBlocks(t *testing.T) {
	plain := []byte("INSERT INTO t FORMAT Native\n\x01\x02")
	body := compressNativeLZ4(t, plain)
	out, qs, err := decompressRequestBody(body, "", "database=db&decompress=1", 0)
	require.NoError(t, err)
	assert.Equal(t, plain, out)
	assert.Equal(t, "database=db", qs)
	assert.False(t, strings.Contains(qs, "decompress"))
}

func TestDecompressRequestBody_TooLarge(t *testing.T) {
	plain := bytes.Repeat([]byte("x"), 100)
	body := compressGzip(t, plain)
	_, _, err := decompressRequestBody(body, "gzip", "", 50)
	require.ErrorIs(t, err, ErrRequestTooLarge)
}

func TestDecompressRequestBody_UnsupportedEncoding(t *testing.T) {
	_, _, err := decompressRequestBody([]byte("x"), "unknown", "", 0)
	require.Error(t, err)
}

func TestQueryParamHelpers(t *testing.T) {
	assert.True(t, queryParamEnabled("decompress=1&database=db", "decompress"))
	assert.True(t, queryParamEnabled("decompress=true", "decompress"))
	assert.False(t, queryParamEnabled("decompress=0", "decompress"))
	assert.Equal(t, "database=db", stripQueryParam("database=db&decompress=1", "decompress"))
	assert.Equal(t, "", stripQueryParam("decompress=1", "decompress"))
}

func TestNeedsDecompression(t *testing.T) {
	assert.True(t, needsDecompression("gzip", ""))
	assert.True(t, needsDecompression("", "decompress=1"))
	assert.False(t, needsDecompression("", "database=db"))
	assert.False(t, needsDecompression("identity", ""))
}

func TestServer_WriteHandler_DecompressedInsert(t *testing.T) {
	bodyPlain := "INSERT INTO db.t FORMAT TabSeparated\n1"
	body := compressGzip(t, []byte(bodyPlain))

	sender := &recordingSender{}
	c := NewCollector(sender, nil, 1, 1000, 0, true, false, nil)
	srv := InitServer("", c, nil, nil, nil, nil, false, false, false)
	srv.MaxRequestBytes = 0

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/?database=db", bytes.NewReader(body))
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	cctx := e.NewContext(req, rec)

	require.NoError(t, srv.writeHandler(cctx))
	assert.Equal(t, http.StatusOK, rec.Code)

	require.Eventually(t, func() bool {
		return sender.last() != nil
	}, 2*time.Second, 10*time.Millisecond)

	got := sender.last()
	assert.Equal(t, bodyPlain, got.Content)
	assert.NotContains(t, got.Params, "decompress=")
}

func TestServer_WriteHandler_DecompressedOpaqueNative(t *testing.T) {
	plain := append([]byte("INSERT INTO db.t FORMAT Native\n"), 0xca, 0xfe)
	body := compressNativeLZ4(t, plain)

	sender := &recordingSender{}
	c := NewCollector(sender, nil, 1000, 1000, 0, true, false, nil)
	srv := InitServer("", c, nil, nil, nil, nil, false, false, false)

	e := echo.New()
	q := url.QueryEscape("INSERT INTO db.t FORMAT Native")
	req := httptest.NewRequest(http.MethodPost, "/?database=db&query="+q+"&decompress=1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	cctx := e.NewContext(req, rec)

	require.NoError(t, srv.writeHandler(cctx))
	assert.Equal(t, http.StatusOK, rec.Code)

	require.Eventually(t, func() bool {
		return sender.last() != nil
	}, time.Second, 10*time.Millisecond)

	got := sender.last()
	assert.Equal(t, string(plain), got.Content)
	assert.Equal(t, "application/octet-stream", got.ContentType)
	assert.NotContains(t, got.Params, "decompress=")
}

func TestServer_WriteHandler_DecompressFailure(t *testing.T) {
	sender := &recordingSender{}
	c := NewCollector(sender, nil, 1000, 1000, 0, true, false, nil)
	srv := InitServer("", c, nil, nil, nil, nil, false, false, false)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not gzip"))
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	cctx := e.NewContext(req, rec)

	err := srv.writeHandler(cctx)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestServer_WriteHandler_DecompressTooLarge(t *testing.T) {
	plain := bytes.Repeat([]byte("a"), 200)
	body := compressGzip(t, plain)

	sender := &recordingSender{}
	c := NewCollector(sender, nil, 1000, 1000, 0, true, false, nil)
	srv := InitServer("", c, nil, nil, nil, nil, false, false, false)
	srv.MaxRequestBytes = 100

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	cctx := e.NewContext(req, rec)

	err := srv.writeHandler(cctx)
	require.NoError(t, err)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestReadConfig_MaxRequestBytes(t *testing.T) {
	t.Setenv("MAX_REQUEST_BYTES", "4096")
	cnf, err := ReadConfig("non_existent_config.json")
	require.NoError(t, err)
	assert.Equal(t, int64(4096), cnf.MaxRequestBytes)
}
