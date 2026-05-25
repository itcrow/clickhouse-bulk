# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed

- **Docker (hardened runtime):** distroless `static-debian12:nonroot`, non-root UID 65532, stripped static binary, `.dockerignore`; [docs/DOCKER.md](docs/DOCKER.md) run examples use `-config=…` args (no `./clickhouse-bulk` in container).

### Added

- **P4.6 Client samples:** [`examples/go_direct_ch.go`](../examples/go_direct_ch.go), [`examples/python_raw_insert.py`](../examples/python_raw_insert.py), [`examples/README.md`](../examples/README.md).
- **P5.4 Low-ROI tests:** `runCLI()` (`main_test.go`); `runServer` SIGTERM e2e (`runserver_e2e_test.go`); removed leaky `RunServer` goroutine from `TestServer_MultiServer`.
- **P4.4 Hybrid batch formats:** config `batch_formats` / `BATCH_FORMATS`; listed FORMAT names batched, others opaque passthrough. Tests in `batch_formats_test.go`.
- **P4.5 Sync INSERT:** config `sync_insert` / `SYNC_INSERT` or header `X-Bulk-Sync: 1`; inline `SendQuery` to live; CH status/body/headers returned; journal ack after success/dump; dual-write backup async on live success. Tests in `sync_insert_test.go`.
- **P4.3 Response header forwarding:** proxied (`SELECT`/DDL) queries return ClickHouse response headers to the client (`X-ClickHouse-Query-Id`, `X-ClickHouse-Summary`, `Content-Type`, …). `SendQuery` returns `http.Header`; `writeHandler` uses `writeProxiedQueryResponse`. Tests in `response_headers_test.go`.
- **P4.2 Request decompression:** inbound `Content-Encoding` (gzip, deflate, zstd, lz4, snappy, br) and ClickHouse native blocks (`decompress=1`); config `max_request_bytes` / `MAX_REQUEST_BYTES` (default 128 MiB). Plain body forwarded to CH; `decompress=1` stripped from outbound params. Tests in `decompress_test.go`.
- **P4.1 Opaque INSERT passthrough:** auto for `FORMAT Native` / `RowBinary` / `Parquet` / `Arrow` / … and `Content-Type: application/octet-stream`; config `opaque_insert` / `OPAQUE_INSERT` forces all INSERTs through passthrough. No batch merge; journal stores binary body as base64; outbound POST preserves `Content-Type`.
- **P5 test coverage (~83%):** ops HTTP, journal/Run/dump paths, collector/metrics/config; new test files `server_ops_test`, `clickhouse_run_test`, `collector_extended_test`, `journal_extended_test`, `dump_extended_test`, `metrics_test`.
- [docs/DOCKER.md](docs/DOCKER.md) — Docker Hub link, run/how-to, `docker push itcrow/clickhouse-bulk:tagname`.
- Docs: [docs/CLIENT_COMPATIBILITY.md](docs/CLIENT_COMPATIBILITY.md) (clickhouse-go, clickhouse-connect); roadmap in [docs/ROADMAP.md](docs/ROADMAP.md).
- Dependabot: `.github/dependabot.yml` (gomod, github-actions, docker; weekly).
- **Live / backup dual-write** when `clickhouse-backup.servers` or `CLICKHOUSE_BACKUP_SERVERS` is set.
- `DualSender`, separate queues, dumps (`dump_dir`, `bkp_dump_dir`), and replay loops.
- **Journal (WAL):** `journal_dir`, `journal_fsync`, `max_journal_pending`; ack on live send or live dump; replay on startup; metrics `ch_journal_pending`, `ch_journal_dir_bytes`.
- **Send rate limit:** `send_max_rps`, `send_max_burst` per `clickhouse` and `clickhouse-backup`.
- `clickhouse-backup.query_params`, `bkp_dump_check_interval`, `dump_replay_batch`, `max_dump_files`, `shutdown_drain_sec`.
- `GET /status` with live/backup health.
- `POST|GET /debug/replay-failed` — replay dumps from `dump_dir/failed/` (and `bkp_dump_dir/failed/`).
- Prometheus: `ch_bkp_*`, `ch_send_queue`, `ch_dump_dir_bytes`, `ch_last_sent_unixtime`, etc.
- Docs: [docs/DUAL_WRITE.md](docs/DUAL_WRITE.md), [docs/RISKS.md](docs/RISKS.md), [docs/CONFIG.md](docs/CONFIG.md), [docs/ALERTS.md](docs/ALERTS.md).
- Samples: `config.sample.json` (live), `config.sample-backup.json` (dual-write).

### Fixed

- Validate `journal_dir`, `dump_dir`, `bkp_dump_dir` at startup (reject `..` path traversal).
- Sanitize dump file ids in `GetDumpData` / `DeleteDump` / replay (`failed/<basename>` only).
- Redact passwords/tokens in logs; do not log full INSERT body on empty insert / errors.
- Graceful shutdown: HTTP stop → `SafeQuit` → drain live/backup queues (`shutdown_drain_sec`).
- 4xx dumps moved to `failed/` (no infinite retry).
- `CleanTables` / mutex fixes; server URL validation and trim.
- Backup metrics registered only when dual-write is enabled.

### Changed

- `Plan.md` renamed and moved to [docs/ROADMAP.md](docs/ROADMAP.md).
- Go **1.26.3**; dependencies updated (echo v4.15.2, prometheus client_golang v1.23.2, testify v1.11.1, golang.org/x/*).
- Go module: `github.com/itcrow/clickhouse-bulk` (was `github.com/nikepan/clickhouse-bulk`).
- `RunServer` builds per-target senders; backup wrapped in `DualSender`.
- Journal ack when live stores batch (CH **or** `dump_dir`), not backup-only.

### Notes

- Dual-write is **best-effort per target**, not synchronous replication. See [docs/ROADMAP.md](docs/ROADMAP.md).
- Fork base: upstream **v1.3.9** ([nikepan/clickhouse-bulk](https://github.com/nikepan/clickhouse-bulk)).

---

## Upstream (nikepan/clickhouse-bulk)

See [upstream releases](https://github.com/nikepan/clickhouse-bulk/releases) for history before this fork.
