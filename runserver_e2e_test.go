package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func freeListenAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func waitHTTPReady(t *testing.T, url string) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond)
}

func TestRunServer_ShutdownOnSignal(t *testing.T) {
	var posts atomic.Int32
	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		posts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ch.Close()

	t.Setenv("CLICKHOUSE_SERVERS", ch.URL)
	t.Setenv("JOURNAL_DIR", t.TempDir())
	t.Setenv("DUMP_DIR", t.TempDir())
	t.Setenv("DUMP_CHECK_INTERVAL", "-1")

	cnf, err := ReadConfig("non_existent_config.json")
	require.NoError(t, err)
	cnf.Listen = freeListenAddr(t)
	cnf.ShutdownDrainSec = 5
	cnf.FlushCount = 1
	cnf.FlushInterval = 1000

	signals := make(chan os.Signal, 1)
	exitCh := make(chan int, 1)
	go runServer(cnf, signals, func(code int) { exitCh <- code })

	base := "http://" + cnf.Listen
	waitHTTPReady(t, base+"/status")

	body := "INSERT INTO db.t FORMAT TabSeparated\n1"
	resp, err := http.Post(base+"/", "text/plain", strings.NewReader(body))
	require.NoError(t, err)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	signals <- syscall.SIGTERM

	select {
	case code := <-exitCh:
		assert.Equal(t, 0, code)
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown timeout")
	}

	assert.Eventually(t, func() bool { return posts.Load() >= 1 }, 5*time.Second, 50*time.Millisecond)
}
