# Roadmap and known issues

Code review of clickhouse-bulk (live + `clickhouse-backup` mode).  
Dual-write semantics: **asynchronous replication with at-least-once delivery per target**, not a guarantee that live ≡ backup.

See [DUAL_WRITE.md](./DUAL_WRITE.md), [RISKS.md](./RISKS.md), [CONFIG.md](./CONFIG.md), [ALERTS.md](./ALERTS.md), and [CLIENT_COMPATIBILITY.md](./CLIENT_COMPATIBILITY.md).

---

## P0 — critical (operations / data loss)

### 1. HTTP `200 OK` before data is actually written — **done**

- **Status:** ✅ **Journal (WAL)** when `journal_dir` is set: append before `200`, replay unacked on startup, `ack` when live batch is on ClickHouse **or** in `dump_dir` (not backup-only). Empty `journal_dir` = legacy behavior.

### 2. Graceful shutdown does not drain queues — **done**

- **Status:** ✅ HTTP shutdown → `SafeQuit` → exit; drain timeout via `shutdown_drain_sec`.

### 3. No coordination between live and backup — **done (docs + metrics)**

- **Status:** ✅ [DUAL_WRITE.md](./DUAL_WRITE.md), [ALERTS.md](./ALERTS.md); metrics `ch_last_sent_unixtime`, `ch_bkp_last_sent_unixtime` for lag heuristics.

### 4. 4xx errors in dumps — infinite retry — **done**

- **Status:** ✅ Dumps with filename kind `2` (4xx) moved to `<dump_dir>/failed/`, not retried.

---

## P1 — high (backup / config / observability)

### 5. Double memory and load in backup mode — **documented**

- **Status:** ✅ Documented in [DUAL_WRITE.md](./DUAL_WRITE.md); queue size 1000 per target.

### 6. Dump replay bypasses the send queue — **open**

- **Status:** ⬜ Replay stays synchronous via `SendQuery` (correct delete-after-success). Rate limit optional later.

### 7. Single `params` set for live and backup — **done**

- **Status:** ✅ `clickhouse-backup.query_params` / `CLICKHOUSE_BACKUP_QUERY_PARAMS`.

### 8. `config.sample.json` always enables backup — **done**

- **Status:** ✅ Backup example moved to `config.sample-backup.json`.

### 9. Env: spaces in server lists — **done**

- **Status:** ✅ `splitTrimServers` / `normalizeServerList`.

### 10. Env vs file order for backup TLS — **done**

- **Status:** ✅ Documented in [DUAL_WRITE.md](./DUAL_WRITE.md#configuration-precedence).

---

## P2 — medium (code quality / UX)

### 11. `defer delete` inside a loop (`CleanTables`) — **done**

- **Status:** ✅ Collect keys, delete after loop (`collector.go`, `tablesCleanHandler`).

### 12. `CleanTable`: `t = nil` does not remove from the map — **done**

- **Status:** ✅ Caller deletes from map; `CleanTable` only stops ticker.

### 13. `Table.Flush` while holding the mutex — **done**

- **Status:** ✅ `doFlush` releases lock before `Sender.Send`.

### 14. `/status` does not expose backup — **done**

- **Status:** ✅ `GET /status` returns `FullStatus` with `live` and `backup` targets.

### 15. `WaitFlush` / `wg` on queue failure — **done**

- **Status:** ✅ `SafeQuit` honors `shutdown_drain_sec` timeout.

---

## P3 — low (observability / convenience)

### 16. Empty URL in `servers` — **done**

- **Status:** ✅ `validateClickhouseConfig` at startup.

### 17. No limit on dump directory size — **done**

- **Status:** ✅ `max_dump_files` prunes oldest pending `.dmp` files; metrics `ch_dump_dir_bytes`, `ch_bkp_dump_dir_bytes`.

### 18. `LockedFiles` on delete failure — **done**

- **Status:** ✅ `DeleteDump` retries remove 3 times.

### 19. No health check for “backup lagging behind live” — **done**

- **Status:** ✅ `ch_last_sent_unixtime` vs `ch_bkp_last_sent_unixtime`; alert example in [ALERTS.md](./ALERTS.md).

### 20. `ch_bkp_*` metrics when backup mode is off — **done**

- **Status:** ✅ `InitMetrics(prefix, backupEnabled)` registers backup collectors only when dual-write is on.

### 21. Parallel I/O from two dump `Listen` loops — **done**

- **Status:** ✅ `bkp_dump_check_interval` (fallback: `dump_check_interval`); `dump_replay_batch` caps files per replay tick.

---

## Dual-write scenarios (reference)

| Situation | Outcome |
|-----------|---------|
| Live OK, backup down | Data on live; backup catches up from `bkp_dump_dir` |
| Live down → dump, backup OK | Data on backup; live catches up from `dump_dir` |
| Live OK, backup OK, different latency | Temporary replica divergence |
| Different schema/permissions on backup | 4xx → `bkp_dump_dir/failed/` |

---

## Recommended work order

1. ~~P0.02 shutdown~~ ✅  
2. ~~P0.03 docs/alerts~~ ✅  
3. ~~P0.04 4xx DLQ~~ ✅  
4. ~~P1.07–10 config~~ ✅  
5. ~~P2.11–15 code/UX~~ ✅  
6. P1.06 replay rate limit (optional)  
7. ~~P0.01 journal~~ ✅  
8. ~~P3.17, P3.20–21~~ ✅  
9. ~~P5.1 ops HTTP tests~~ ✅  
10. ~~P5.2 journal / dump / Run worker tests~~ ✅  
11. ~~P5.3 Parse / metrics / config edges~~ ✅  
12. ~~P4.2 decompression (client compatibility)~~ ✅  
13. ~~P4.3 response headers~~ ✅  
14. ~~P4.5 sync insert~~ ✅  
15. ~~P4.4 hybrid batch formats~~ ✅  

---

## Live/backup implementation status

| Component | Status |
|-----------|--------|
| `DualSender` | ✅ |
| Separate dumps + replay | ✅ |
| Metrics + last-sent timestamps | ✅ |
| `/status` live + backup | ✅ |
| `query_params` for backup | ✅ |
| `config.sample-backup.json` | ✅ |
| Journal (P0.01) | ✅ |
| Roadmap items (open above) | P1.06 |
| Test coverage (P5) | ✅ ~83%; see [P5 — Test coverage](#p5--test-coverage-optional) |

---

## P4 — Client compatibility (optional)

Goal: improve interoperability with [clickhouse-go](https://github.com/ClickHouse/clickhouse-go) and [clickhouse-connect](https://clickhouse.com/docs/integrations/python) **without** turning bulk into a full HTTP proxy. Current behaviour: [CLIENT_COMPATIBILITY.md](./CLIENT_COMPATIBILITY.md).

Design principle: **default path unchanged** (batched text INSERT for Vector/curl); new behaviour behind config flags.

### P4.1 — Opaque INSERT passthrough — **done**

- **Status:** ✅ Auto-detect (`application/octet-stream`, `FORMAT Native` / `RowBinary` / `Parquet` / `Arrow` / … in `query=`) or `opaque_insert: true` for every INSERT. Skips collector batching; optional journal (`AppendOpaque`, base64 body); outbound POST preserves client `Content-Type` (default `application/octet-stream` for binary formats). Async `200` unchanged.
- **Unlocks:** clickhouse-go HTTP `PrepareBatch`; connect `insert()` payload pass-through (use P4.5 sync for CH errors).

### P4.2 — Request decompression — **done**

- **Status:** ✅ In `writeHandler`, before opaque/batched routing: HTTP `Content-Encoding` (`gzip`, `deflate`, `zstd`, `lz4`, `snappy`, `br`) and ClickHouse native block compression when `decompress=1` in query params. Config `max_request_bytes` / `MAX_REQUEST_BYTES` (default 128 MiB; `0` = unlimited). Outbound to CH: plain body; `decompress=1` stripped from params. Errors: HTTP 400 (bad compression), 413 (too large).
- **Unlocks:** clickhouse-go HTTP with `CompressionLZ4` / `CompressionZSTD` / gzip; clickhouse-connect with `compress=True`.

### P4.3 — Response header forwarding — **done**

- **Status:** ✅ Proxied queries (`SendQuery`, non-INSERT): ClickHouse response headers copied to the client (`X-ClickHouse-*`, `Content-Type`, …). Hop-by-hop and `Content-Length` / `Content-Encoding` are not forwarded. Sync INSERT (P4.5) also returns CH headers.
- **Unlocks:** clickhouse-connect `query()` / `command()` reading `X-ClickHouse-Summary`, `X-ClickHouse-Query-Id`.

### P4.4 — Hybrid batch formats (config) — **done**

- **Status:** ✅ Config `batch_formats` / `BATCH_FORMATS` (comma-separated or JSON array). When non-empty: only listed `FORMAT` names are batched; other INSERTs use opaque passthrough (P4.1). Unset = legacy behavior (auto opaque for Native/RowBinary/… and `application/octet-stream`). Case-insensitive format match; `VALUES` inserts match `Values`.
- **Unlocks:** ETL on TabSeparated batching + app drivers on Native passthrough in one bulk instance.

### P4.5 — Optional synchronous INSERT — **done**

- **Status:** ✅ Config `sync_insert` / `SYNC_INSERT` or request header `X-Bulk-Sync: 1` (also `true`/`yes`/`on`). Skips batching; `LiveSender.SendQuery` inline; returns CH status/body/headers (P4.3). Journal append before send; ack after live success or dump (same as async worker). Dual-write: **sync live only**, backup enqueued async on live success only.
- **Unlocks:** clickhouse-go / connect drivers that expect INSERT errors in HTTP response (throughput cost — document as low-rate / debug).

### P4.6 — Documentation & samples — **done**

- **Status:** ✅ [CLIENT_COMPATIBILITY.md](./CLIENT_COMPATIBILITY.md). Runnable samples in [`examples/`](../examples/): `go_direct_ch.go` (clickhouse-go → ClickHouse native, bypass bulk), `python_raw_insert.py` (clickhouse-connect `raw_insert` → bulk, async).

### Recommended implementation order

1. P4.6 (docs) ✅  
2. ~~P4.1 opaque passthrough~~ ✅  
3. ~~P4.2 decompression~~ ✅  
4. ~~P4.4 hybrid formats~~ ✅  
5. ~~P4.3 headers (proxied)~~ ✅  
6. ~~P4.5 sync insert~~ ✅  

### Non-goals

- Native TCP on bulk port.
- Merging multiple Native INSERT bodies into one batch.
- Exactly-once or full `clickhouse-connect` feature parity (sessions, temporary tables, external data) without explicit design.

---

## P5 — Test coverage (optional)

**Baseline:** `go test -cover ./...` ≈ **83%** (CI also runs `-race` on Coverage step).  
**Test files:** `collector_test`, `collector_extended_test`, `opaque_test`, `batch_formats_test`, `decompress_test`, `sync_insert_test`, `response_headers_test`, `runserver_e2e_test`, `main_test`, `dump_*`, `dump_extended_test`, `journal_*`, `journal_extended_test`, `config_test`, `server_test`, `server_ops_test`, `clickhouse_test`, `clickhouse_run_test`, `dual_sender_test`, `rate_limiter_test`, `metrics_test`, `utils_log_test`.

### P5.1 — Ops HTTP API — **done**

| Area | Covered in |
|------|------------|
| `GET /status` | `server_ops_test.go` — live/backup queue, servers |
| `POST\|GET /debug/replay-failed` | `server_ops_test.go` — live/backup/all, limit, errors |
| `GET /debug/tables-clean` | `server_ops_test.go` |
| `DualSender` | `server_ops_test.go`, `dual_sender_test.go` — `SendQuery`, `WaitFlush`, `Empty` |

### P5.2 — Journal, ClickHouse worker, dumps — **done**

| Area | Covered in |
|------|------------|
| Journal | `journal_extended_test.go` — fsync/reopen, compact, corrupt WAL, `DirBytes` |
| `clickhouse.Run` | `clickhouse_run_test.go` — `ackJournal`, dump on 4xx, retry, `mergeQueryParams` |
| Dump replay | `dump_extended_test.go` — `ReplayFailed` limit, `Listen`, `DeleteDump`, `parseDumpPayload` |
| Server shutdown | `server_ops_test.go` — journal 503/500, `SafeQuit` timeout |

### P5.3 — Collector, metrics, config edges — **done**

| Area | Covered in |
|------|------------|
| `Parse` / batching | `collector_extended_test.go` — VALUES, RowBinary, `remove_query_id`, timers |
| Opaque edge | `opaque_test.go` — BasicAuth, body-only INSERT |
| Metrics | `metrics_test.go` — counters/gauges, backup gating, journal |
| Config | `config_test.go`, `server_ops_test.go` — validation, `backupDumpCheckInterval` |

### P5.4 — Low ROI / deferred — **done**

- **`main()`:** `runCLI()` extracted; tests in `main_test.go` (`version`, config validation error, bad flag).
- **`RunServer` SIGTERM e2e:** `runServer(cnf, signals, exit)` hook; `runserver_e2e_test.go` — insert, signal, drain to ClickHouse, exit code 0.
- Removed leaky `go RunServer()` from `TestServer_MultiServer`.
- P4 feature tests added in prior P4 work (`batch_formats_test`, `sync_insert_test`, `decompress_test`, `response_headers_test`).

### Recommended test order

1. ~~P5.1~~ ✅  
2. ~~P5.2~~ ✅  
3. ~~P5.3~~ ✅  
4. ~~P5.4~~ ✅  

**CI:** keep `go test -race -coverprofile=coverage.out` in [`.github/workflows/test.yml`](../.github/workflows/test.yml); optional coverage threshold gate later.
