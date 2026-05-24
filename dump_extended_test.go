package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileDumper_effectiveReplayBatch(t *testing.T) {
	d := NewDumper(t.TempDir())
	assert.Equal(t, 0, d.effectiveReplayBatch(0))
	assert.Equal(t, 5, d.effectiveReplayBatch(5))
}

func TestParseDumpPayload(t *testing.T) {
	params, query, content, rows := parseDumpPayload("user=1&query=INSERT\nINSERT INTO t FORMAT TabSeparated\n1\t2\n3\t4")
	assert.Equal(t, "user=1&query=INSERT", params)
	assert.Equal(t, "INSERT INTO t FORMAT TabSeparated", query)
	assert.Contains(t, content, "1\t2")
	assert.Equal(t, 2, rows)

	_, _, _, rows0 := parseDumpPayload("INSERT INTO t FORMAT Native\n\x01\x02")
	assert.Equal(t, 1, rows0)
}

func TestDump_ReplayFailed_Limit(t *testing.T) {
	dumper := NewDumper(t.TempDir())
	dumper.DumpPrefix = "20990101120000"
	for i := 0; i < 3; i++ {
		dumper.DumpNum = i
		require.NoError(t, dumper.Dump("p=1", "row", "err", dumpKindClientError, 400))
		require.NoError(t, dumper.moveToFailed(dumper.dumpName(i+1, dumpKindClientError, 400)))
	}

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	sender := NewClickhouse(10, 10, "", false, 0, 0)
	sender.AddServer(ok.URL, false)

	report := dumper.ReplayFailed(sender, 1)
	assert.Equal(t, 1, report.Sent)
	assert.Equal(t, 2, report.Remaining)
}

func TestDump_Listen_ReplayBatch(t *testing.T) {
	dumpDir := t.TempDir()
	dumper := NewDumper(dumpDir)
	dumper.DumpPrefix = "20990101120000"
	dumper.DumpNum = 0
	require.NoError(t, dumper.Dump("p", "row", "err", dumpKindTransient, 502))

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	sender := NewClickhouse(10, 10, "", false, 0, 0)
	sender.AddServer(ok.URL, false)

	dumper.Listen(sender, 1, 1)
	time.Sleep(2500 * time.Millisecond)

	files, err := dumper.listPendingDumpFiles()
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestDump_DeleteDump_Retries(t *testing.T) {
	dumpDir := t.TempDir()
	dumper := NewDumper(dumpDir)
	dumper.DumpPrefix = "20990101120000"
	dumper.DumpNum = 0
	require.NoError(t, dumper.Dump("p", "data", "", dumpKindTransient, 502))
	name := dumper.dumpName(1, dumpKindTransient, 502)

	full, err := dumper.makePath(name)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(full, 0400))
	require.NoError(t, os.Chmod(dumpDir, 0500))

	err = dumper.DeleteDump(name)
	assert.Error(t, err)

	require.NoError(t, os.Chmod(dumpDir, 0700))
	require.NoError(t, os.Chmod(full, 0600))
	require.NoError(t, os.Remove(full))
}

func TestDump_GetDump_SkipsFailedSubdir(t *testing.T) {
	dumper := NewDumper(t.TempDir())
	dumper.DumpPrefix = "20990101120000"
	dumper.DumpNum = 0
	require.NoError(t, dumper.Dump("pending", "row", "", dumpKindTransient, 502))
	require.NoError(t, dumper.Dump("failed", "row", "err", dumpKindClientError, 400))
	require.NoError(t, dumper.moveToFailed(dumper.dumpName(2, dumpKindClientError, 400)))

	name, err := dumper.GetDump()
	require.NoError(t, err)
	assert.Equal(t, dumper.dumpName(1, dumpKindTransient, 502), name)

	_, err = os.Stat(path.Join(dumper.Path, failedDumpSubdir))
	require.NoError(t, err)
}
