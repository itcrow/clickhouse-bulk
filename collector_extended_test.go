package main

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollector_Parse_ValuesAndFormatBranches(t *testing.T) {
	c := NewCollector(&fakeSender{}, nil, 1000, 1000, 0, true, false, nil)

	prefix, content := c.Parse(qValuesTitle + " " + qValuesContent)
	assert.True(t, HasPrefix(prefix, "insert"))
	assert.Contains(t, strings.ToLower(prefix), "values")
	assert.Equal(t, qValuesContent, content)

	prefix, content = c.Parse(qFormatInQuotesQuery + " " + qFormatInQuotesValues)
	assert.Contains(t, prefix, "VALUES")
	assert.Equal(t, qFormatInQuotesValues, content)

	prefix, content = c.Parse(qTSNamesTitle + "\n" + qNames + "\n" + qContent)
	assert.Contains(t, prefix, "TabSeparatedWithNames")
	assert.Contains(t, content, qNames)
}

func TestTable_Content_RowBinary(t *testing.T) {
	tbl := NewTable("k", &fakeSender{}, 10, 1000)
	tbl.Query = "INSERT INTO t FORMAT RowBinary"
	tbl.Format = "RowBinary"
	tbl.Rows = []string{"\x01\x02", "\x03\x04"}
	tbl.count = 2

	out := tbl.Content()
	assert.True(t, len(out) > len(tbl.Query))
	assert.NotContains(t, out, "\n\x03") // RowBinary joins without newline between rows
}

func TestCollector_RemoveQueryID_BatchesSameTable(t *testing.T) {
	sender := &recordingSender{}
	c := NewCollector(sender, nil, 2, 1000, 0, true, false, nil)

	base := url.QueryEscape("INSERT INTO t FORMAT TabSeparated")
	c.Push("query_id=q1&query="+base, "a\tb", 0)
	c.Push("query_id=q2&query="+base, "c\td", 0)

	require.Len(t, sender.requests, 1)
	assert.False(t, sender.requests[0].opaque)
	assert.Contains(t, sender.requests[0].Content, "a\tb")
	assert.Contains(t, sender.requests[0].Content, "c\td")
}

func TestCollector_CleanTables_RemovesIdle(t *testing.T) {
	sender := &fakeSender{}
	c := NewCollector(sender, nil, 1000, 50, 100, true, false, nil)
	c.AddTable("idle")

	c.mu.Lock()
	for _, tbl := range c.Tables {
		tbl.lastUpdate = time.Now().Add(-200 * time.Millisecond)
	}
	c.mu.Unlock()

	c.CleanTables()

	c.mu.RLock()
	n := len(c.Tables)
	c.mu.RUnlock()
	assert.Equal(t, 0, n)
}

func TestCollector_TableTimerFlush(t *testing.T) {
	sender := &recordingSender{}
	c := NewCollector(sender, nil, 1000, 50, 0, true, false, nil)
	params := "query=" + url.QueryEscape("INSERT INTO t FORMAT TabSeparated")
	c.Push(params, "1\t2", 0)

	deadline := time.Now().Add(500 * time.Millisecond)
	for sender.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.NotEmpty(t, sender.requests)
}

func TestCollector_Flush(t *testing.T) {
	sender := &recordingSender{}
	c := NewCollector(sender, nil, 1000, 1000, 0, true, false, nil)
	params := "query=" + url.QueryEscape("INSERT INTO t FORMAT TabSeparated")
	c.Push(params, "1\t2", 0)
	c.FlushAll()
	require.Len(t, sender.requests, 1)
}

func TestTable_CleanTableStopsTimer(t *testing.T) {
	tbl := NewTable("k", &fakeSender{}, 10, 50)
	tbl.TickerChan = tbl.RunTimer()
	tbl.CleanTable()
	assert.Nil(t, tbl.TickerChan)
}
