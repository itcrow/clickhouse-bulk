package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInsertFormatFromQuery(t *testing.T) {
	params := "query=" + url.QueryEscape("INSERT INTO t FORMAT TabSeparated")
	assert.Equal(t, "TabSeparated", insertFormatFromQuery(params, nil))

	body := []byte("INSERT INTO t FORMAT JSONEachRow\n{}\n")
	assert.Equal(t, "JSONEachRow", insertFormatFromQuery("", body))

	assert.Equal(t, formatValues, insertFormatFromQuery("", []byte("INSERT INTO t VALUES (1)")))
}

func TestIsListedBatchFormat(t *testing.T) {
	formats := []string{"TabSeparated", "Values", "JSONEachRow"}
	assert.True(t, isListedBatchFormat(formats, "tabseparated"))
	assert.True(t, isListedBatchFormat(formats, "VALUES"))
	assert.False(t, isListedBatchFormat(formats, "Native"))
}

func TestShouldOpaqueInsert_HybridMode(t *testing.T) {
	batch := []string{"TabSeparated", "Values", "JSONEachRow"}
	body := []byte{0x01}

	tsv := url.QueryEscape("INSERT INTO t FORMAT TabSeparated")
	assert.False(t, shouldOpaqueInsert(false, batch, "", "query="+tsv, []byte("1\n")))

	native := url.QueryEscape("INSERT INTO t FORMAT Native")
	assert.True(t, shouldOpaqueInsert(false, batch, "", "query="+native, body))

	rowbin := url.QueryEscape("INSERT INTO t FORMAT RowBinary")
	assert.True(t, shouldOpaqueInsert(false, batch, "application/octet-stream", "query="+rowbin, body))

	jsonEach := url.QueryEscape("INSERT INTO t FORMAT JSONEachRow")
	assert.False(t, shouldOpaqueInsert(false, batch, "", "query="+jsonEach, []byte("{}\n")))

	values := url.QueryEscape("INSERT INTO t VALUES (1)")
	assert.False(t, shouldOpaqueInsert(false, batch, "", "query="+values, nil))
}

func TestShouldOpaqueInsert_HybridMode_LegacyBinaryStillOpaqueWithoutList(t *testing.T) {
	body := []byte{0x01}
	native := url.QueryEscape("INSERT INTO t FORMAT Native")
	assert.True(t, shouldOpaqueInsert(false, nil, "", "query="+native, body))
}

func TestServer_HybridNative_OpaquePath(t *testing.T) {
	sender := &recordingSender{}
	batch := []string{"TabSeparated", "Values", "JSONEachRow"}
	c := NewCollector(sender, nil, 1000, 1000, 0, true, false, batch)
	srv := InitServer("", c, nil, nil, nil, nil, false, false, false)

	e := echo.New()
	q := url.QueryEscape("INSERT INTO db.t FORMAT Native")
	payload := string([]byte{0xca, 0xfe})
	req := httptest.NewRequest(http.MethodPost, "/?query="+q, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	require.NoError(t, srv.writeHandler(e.NewContext(req, rec)))
	assert.Equal(t, http.StatusOK, rec.Code)

	require.Eventually(t, func() bool {
		return sender.last() != nil
	}, time.Second, 10*time.Millisecond)

	got := sender.last()
	assert.True(t, got.opaque)
	assert.Equal(t, payload, got.Content)
	assert.Equal(t, "application/octet-stream", got.ContentType)
}

func TestServer_HybridTabSeparated_BatchedPath(t *testing.T) {
	sender := &recordingSender{}
	batch := []string{"TabSeparated", "Values", "JSONEachRow"}
	c := NewCollector(sender, nil, 1, 1000, 0, true, false, batch)
	srv := InitServer("", c, nil, nil, nil, nil, false, false, false)

	e := echo.New()
	body := "INSERT INTO db.t FORMAT TabSeparated\n42"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	require.NoError(t, srv.writeHandler(e.NewContext(req, rec)))
	assert.Equal(t, http.StatusOK, rec.Code)

	require.Eventually(t, func() bool {
		return sender.last() != nil
	}, time.Second, 10*time.Millisecond)

	got := sender.last()
	assert.False(t, got.opaque)
	assert.Contains(t, got.Content, "FORMAT TabSeparated")
	assert.Contains(t, got.Content, "42")
}

func TestServer_HybridJSONEachRow_OpaqueWhenNotListed(t *testing.T) {
	sender := &recordingSender{}
	batch := []string{"TabSeparated", "Values"}
	c := NewCollector(sender, nil, 1000, 1000, 0, true, false, batch)
	srv := InitServer("", c, nil, nil, nil, nil, false, false, false)

	e := echo.New()
	q := url.QueryEscape("INSERT INTO db.t FORMAT JSONEachRow")
	req := httptest.NewRequest(http.MethodPost, "/?query="+q, bytes.NewReader([]byte(`{"x":1}`+"\n")))
	rec := httptest.NewRecorder()
	require.NoError(t, srv.writeHandler(e.NewContext(req, rec)))

	require.Eventually(t, func() bool {
		return sender.last() != nil
	}, time.Second, 10*time.Millisecond)
	assert.True(t, sender.last().opaque)
}

func TestReadConfig_BatchFormats(t *testing.T) {
	t.Setenv("BATCH_FORMATS", "TabSeparated, Values, JSONEachRow")
	cnf, err := ReadConfig("non_existent_config.json")
	require.NoError(t, err)
	require.Len(t, cnf.BatchFormats, 3)
	assert.Equal(t, "TabSeparated", cnf.BatchFormats[0])
	assert.Equal(t, "Values", cnf.BatchFormats[1])
	assert.Equal(t, "JSONEachRow", cnf.BatchFormats[2])
}
