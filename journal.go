package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const walFileName = "wal.jsonl"
const ackFileName = "ack.jsonl"

// Deferred compact limits (P5.1 — avoid full WAL rewrite on every Ack).
const (
	journalCompactMinInterval = 5 * time.Second
	journalCompactMinAcks     = 100
)

// ErrJournalBacklog is returned when max_journal_pending is exceeded.
var ErrJournalBacklog = fmt.Errorf("journal backlog limit exceeded")

// Journal is a write-ahead log for accepted INSERT rows (durable before HTTP 200).
type Journal struct {
	dir              string
	fsync            bool
	maxPending       int
	mu               sync.Mutex
	wal              *os.File
	walWriter        *bufio.Writer
	ackFile          *os.File
	nextID           uint64
	acked            map[uint64]struct{}
	pendingCount     int
	acksSinceCompact int
	lastCompact      time.Time
}

type journalRecord struct {
	ID          uint64 `json:"id"`
	Params      string `json:"params"`
	Content     string `json:"content,omitempty"`
	ContentB64  string `json:"content_b64,omitempty"`
	Opaque      bool   `json:"opaque,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// NewJournal opens or creates a journal directory.
func NewJournal(dir string, fsync bool, maxPending int) (*Journal, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0766); err != nil {
		return nil, err
	}
	j := &Journal{
		dir:          dir,
		fsync:        fsync,
		maxPending:   maxPending,
		acked:        make(map[uint64]struct{}),
		lastCompact:  time.Now(),
	}
	if err := j.loadAcked(); err != nil {
		return nil, err
	}
	if err := j.openWAL(); err != nil {
		return nil, err
	}
	j.walWriter = bufio.NewWriter(j.wal)
	if err := j.openAckFile(); err != nil {
		return nil, err
	}
	n, err := j.countPendingOnDiskLocked()
	if err != nil {
		return nil, err
	}
	j.pendingCount = n
	return j, nil
}

func (j *Journal) walPath() string { return filepath.Join(j.dir, walFileName) }
func (j *Journal) ackPath() string { return filepath.Join(j.dir, ackFileName) }

func (j *Journal) loadAcked() error {
	f, err := os.Open(j.ackPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var id uint64
		if _, err := fmt.Sscanf(sc.Text(), "%d", &id); err == nil {
			j.acked[id] = struct{}{}
		}
	}
	return sc.Err()
}

func (j *Journal) openAckFile() error {
	f, err := os.OpenFile(j.ackPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	j.ackFile = f
	return nil
}

func (j *Journal) openWAL() error {
	f, err := os.OpenFile(j.walPath(), os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	j.wal = f
	return j.scanMaxID()
}

func (j *Journal) scanMaxID() error {
	f, err := os.Open(j.walPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec journalRecord
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.ID > j.nextID {
			j.nextID = rec.ID
		}
	}
	return sc.Err()
}

func (j *Journal) countPendingOnDiskLocked() (int, error) {
	f, err := os.Open(j.walPath())
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec journalRecord
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if _, ok := j.acked[rec.ID]; !ok {
			n++
		}
	}
	return n, sc.Err()
}

func (j *Journal) flushWalLocked() error {
	if j.walWriter == nil {
		return nil
	}
	if err := j.walWriter.Flush(); err != nil {
		return err
	}
	if j.fsync && j.wal != nil {
		return j.wal.Sync()
	}
	return nil
}

func (j *Journal) resetAckFileLocked() error {
	if j.ackFile != nil {
		if err := j.ackFile.Close(); err != nil {
			return err
		}
		j.ackFile = nil
	}
	if err := os.WriteFile(j.ackPath(), nil, 0644); err != nil && !os.IsNotExist(err) {
		return err
	}
	return j.openAckFile()
}

func (j *Journal) maybeCompactLocked() error {
	if j.acksSinceCompact == 0 {
		return nil
	}
	if j.acksSinceCompact < journalCompactMinAcks && time.Since(j.lastCompact) < journalCompactMinInterval {
		return nil
	}
	return j.compactLocked()
}

// DirBytes returns total size of files under the journal directory.
func (j *Journal) DirBytes() (int64, error) {
	var total int64
	err := filepath.Walk(j.dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// Append persists one batched-text insert before returning success to the client.
func (j *Journal) Append(params, content string) (uint64, error) {
	return j.appendRecord(journalRecord{Params: params, Content: content})
}

// AppendOpaque persists one opaque (binary) insert before HTTP 200.
func (j *Journal) AppendOpaque(params, content, contentType string) (uint64, error) {
	rec := journalRecord{
		Params:      params,
		Opaque:      true,
		ContentType: contentType,
		ContentB64:  base64.StdEncoding.EncodeToString([]byte(content)),
	}
	return j.appendRecord(rec)
}

func (j *Journal) appendRecord(rec journalRecord) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.maxPending > 0 && j.pendingCount >= j.maxPending {
		return 0, ErrJournalBacklog
	}
	j.nextID++
	id := j.nextID
	rec.ID = id
	line, err := json.Marshal(rec)
	if err != nil {
		return 0, err
	}
	if _, err := j.walWriter.Write(append(line, '\n')); err != nil {
		return 0, err
	}
	if err := j.flushWalLocked(); err != nil {
		return 0, err
	}
	j.pendingCount++
	return id, nil
}

// Ack marks journal entries as durably stored (sent to ClickHouse or written to dump_dir).
func (j *Journal) Ack(ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ackFile == nil {
		if err := j.openAckFile(); err != nil {
			return err
		}
	}
	ackedNew := 0
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := j.acked[id]; ok {
			continue
		}
		if _, err := fmt.Fprintf(j.ackFile, "%d\n", id); err != nil {
			return err
		}
		j.acked[id] = struct{}{}
		j.pendingCount--
		ackedNew++
	}
	if ackedNew == 0 {
		return nil
	}
	j.acksSinceCompact += ackedNew
	if j.fsync {
		if err := j.ackFile.Sync(); err != nil {
			return err
		}
	}
	return j.maybeCompactLocked()
}

// PendingCount returns WAL records not yet acked.
func (j *Journal) PendingCount() (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.pendingCount, nil
}

// ReplayUnacked pushes all non-acked records into the collector.
func (j *Journal) ReplayUnacked(replay func(journalRecord)) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.flushWalLocked(); err != nil {
		return err
	}
	f, err := os.Open(j.walPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	count := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var rec journalRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			log.Printf("WARN: skip corrupt journal line: %+v\n", err)
			continue
		}
		if _, ok := j.acked[rec.ID]; ok {
			continue
		}
		replay(rec)
		count++
	}
	if count > 0 {
		log.Printf("INFO: journal replay: %+v unacked records\n", count)
	}
	return sc.Err()
}

// Compact rewrites WAL keeping only unacked records and resets ack.log.
func (j *Journal) Compact() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.compactLocked()
}

func (j *Journal) compactLocked() error {
	if err := j.flushWalLocked(); err != nil {
		return err
	}
	f, err := os.Open(j.walPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	pending := make([][]byte, 0)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		var rec journalRecord
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if _, ok := j.acked[rec.ID]; !ok {
			pending = append(pending, append(line, '\n'))
		}
	}
	f.Close()
	if err := sc.Err(); err != nil {
		return err
	}
	tmp := j.walPath() + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	for _, line := range pending {
		if _, err := out.Write(line); err != nil {
			out.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if j.wal != nil {
		j.wal.Close()
		j.wal = nil
	}
	j.walWriter = nil
	if err := os.Rename(tmp, j.walPath()); err != nil {
		return err
	}
	if err := j.openWAL(); err != nil {
		return err
	}
	j.walWriter = bufio.NewWriter(j.wal)
	j.acked = make(map[uint64]struct{})
	j.pendingCount = len(pending)
	j.acksSinceCompact = 0
	j.lastCompact = time.Now()
	if err := j.resetAckFileLocked(); err != nil {
		return err
	}
	if j.fsync {
		if err := j.flushWalLocked(); err != nil {
			return err
		}
	}
	return nil
}

// Close flushes WAL, compacts, and closes journal files.
func (j *Journal) Close() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.flushWalLocked(); err != nil {
		return err
	}
	if err := j.compactLocked(); err != nil {
		return err
	}
	if j.ackFile != nil {
		if err := j.ackFile.Close(); err != nil {
			return err
		}
		j.ackFile = nil
	}
	if j.wal != nil {
		if err := j.wal.Close(); err != nil {
			return err
		}
		j.wal = nil
	}
	j.walWriter = nil
	return nil
}

// JournalEnabled reports whether durable accept is active.
func JournalEnabled(j *Journal) bool {
	return j != nil
}
