# Sustained load test

Optional integration load test simulates **many air-quality sensors** posting **SQL `INSERT … VALUES`** to bulk. By default the fixture runs **dual-write** (live + backup mock ClickHouse). Not run in default CI.

## Payload

Each message is a full ClickHouse SQL statement in the POST body (`text/plain`):

```sql
INSERT INTO air_readings (co, no2, o3, pm1, pm4, pm25, pm10, t, p, voc, h, ch2o, abs_h, co2, ct, noise, light, people, voc_index, nox_index, mac, ts, status)
VALUES (0.033, 0.053, ..., '000000000000', 1780467540, '{"connection":"wifi",...}')
```

Field values follow the production sensor template (with per-MAC jitter). Column `status` is a **quoted JSON string** inside SQL (`'{"connection":"wifi",...}'`), not a JSONEachRow row. See `load_payload.go`.

Override table/columns:

```bash
LOAD_TEST_INSERT_INTO='INSERT INTO my_table (co, no2, mac, ts, status)'
```

## Traffic model

- **`LOAD_TEST_MAC_COUNT`** (default **300**) — unique MACs `000000000000` …
- **`LOAD_TEST_RPS`** — target total messages/sec; sets per-MAC interval to `MAC_COUNT / RPS` (e.g. `300` MACs + `RPS=150` → 2s between posts per MAC)
- **`LOAD_TEST_MAC_INTERVAL`** (default **1s**) — per-MAC period; use **instead of** `LOAD_TEST_RPS`, not together
- Without `LOAD_TEST_RPS`: approximate rate = `MAC_COUNT / INTERVAL` (300 MACs × 1s → ~300 msg/s)
- **Default (async):** each MAC fires on schedule without waiting for HTTP response — sustains `target_rps` even when bulk is slow. Watch `inflight=` in progress logs; high values mean backlog at the client.
- **`LOAD_TEST_SYNC=1`:** block until each POST completes (measures end-to-end latency; effective RPS drops when `max_lat` > interval).
- **`LOAD_TEST_MAX_INFLIGHT`:** cap concurrent HTTP requests (default unlimited). MAC starts are staggered across the interval to avoid burst spikes.

```bash
# 500 msg/s from 300 sensors (~600ms per MAC)
LOAD_TEST=1 LOAD_TEST_RPS=500 LOAD_TEST_MAC_COUNT=300 go test -run TestLoad_SustainedInsert -v .
```

With **`LOAD_TEST_JOURNAL=true`**, bulk serializes WAL appends before HTTP 200 — expect high `max_lat` / `inflight` even when `queue=0` (sender already drained).

## Mock ClickHouse latency

Mock live/backup ClickHouse servers can sleep before responding to simulate slow inserts (batch flush → POST to CH):

```bash
# 200ms on both targets
LOAD_TEST=1 LOAD_TEST_CH_DELAY=200ms go test -run TestLoad_SustainedInsert -v .

# slower backup (dual-write queue buildup on backup only)
LOAD_TEST=1 LOAD_TEST_CH_DELAY_LIVE=50ms LOAD_TEST_CH_DELAY_BACKUP=2s go test -run TestLoad_SustainedInsert -v .

# fixed delay + random jitter per CH request
LOAD_TEST=1 LOAD_TEST_CH_DELAY=100ms LOAD_TEST_CH_DELAY_JITTER=50ms go test -run TestLoad_SustainedInsert -v .
```

Delay applies **after** the request body is read (simulates CH processing time). Watch `queue=` grow when CH is slower than ingest.

## Mock ClickHouse outage (unavailability)

Simulate ClickHouse down / degraded (503 from mock, bulk marks server `Bad`, batches go to dump):

| Mode | Env | Behavior |
|------|-----|----------|
| **always** | `LOAD_TEST_CH_DOWN=always` | Mock returns 503 for whole test |
| **window** | `LOAD_TEST_CH_DOWN=window` + `LOAD_TEST_CH_DOWN_AFTER=15s` + `LOAD_TEST_CH_DOWN_FOR=15s` | Outage in the middle, then recovery |
| **per target** | `LOAD_TEST_CH_DOWN_LIVE=always` / `LOAD_TEST_CH_DOWN_BACKUP=always` | Dual-write: only one side down |

Optional: `LOAD_TEST_CH_DOWN_STATUS=503|502|504`, `LOAD_TEST_EXPECT_CH=0` (skip “CH must receive batches” check).

```bash
make loadtest-ch-down-window    # 15s ok → 15s down → recovery
make loadtest-ch-down-always    # CH never accepts (dumps only)
make loadtest-ch-down-backup    # dual-write: backup down, live ok
```

Fixture defaults: `LOAD_TEST_CH_CONNECT_TIMEOUT=60`, `LOAD_TEST_CH_DOWN_TIMEOUT=10` (override via env).

## Test suite and charts

**Default `make loadtest-suite`** — two **2h** runs (dual-write, 500 RPS, 500 MACs):

| Case | Target |
|------|--------|
| `no-journal` | journal off, backup mock latency 200ms + jitter 0–800ms |
| `journal` | journal on, same backup latency |

Total wall time **~4–5 hours** (2h load + drain each). Progress every **30s**. Requires `pip install matplotlib`.

```bash
pip install matplotlib   # once
make loadtest-suite
# → loadtest-results/<timestamp>/{*.log, metrics.csv, charts/*.png}
```

Single case: `make loadtest-suite-no-journal` or `make loadtest-suite-journal`.

Short dev suite (~6×45s): `make loadtest-suite-quick`.

Customize: `LOADTEST_SUITE_CASES=$'foo:loadtest-short\nbar:loadtest-suite-journal' make loadtest-suite`

Parser reads machine lines `LOAD_PROGRESS …` from test output.

## CPU and memory profiling

**Live stats** in progress logs (`LOAD_TEST_MEMSTATS=1`, or automatically with `LOAD_TEST_PROFILE=1`):

```
load ... queue=12 live_batches=120 heap_alloc=45.2MiB heap_inuse=38.1MiB sys=62.0MiB goroutines=523 num_gc=12 cpu_cores=1.15
```

- `heap_alloc` / `heap_inuse` / `sys` — Go runtime memory (`runtime.ReadMemStats`)
- `goroutines` — active goroutines
- `cpu_cores` — average CPU cores used since last progress tick (`runtime/metrics`)

**pprof files** (`LOAD_TEST_PROFILE=1` or set `LOAD_TEST_PROFILE_DIR`):

| File | When |
|------|------|
| `cpu.prof` | CPU during load phase |
| `heap-load.prof` | Heap right after load stops |
| `heap-drain.prof` | Heap after queue drain |

```bash
LOAD_TEST=1 LOAD_TEST_PROFILE=1 LOAD_TEST_PROFILE_DIR=/tmp/bulk-load \
  go test -run TestLoad_SustainedInsert -v .

go tool pprof -http=:6060 /tmp/bulk-load/cpu.prof
go tool pprof -http=:6061 /tmp/bulk-load/heap-drain.prof
```

Without `LOAD_TEST_PROFILE_DIR`, profiles go to a timestamped dir under `$TMPDIR` (path logged at start).

## Mode

- **Default:** dual-write — each batch goes to live and backup mock servers (`DualSender`).
- **Live only:** set `LOAD_TEST_LIVE_ONLY=1` or run `TestLoad_SustainedInsert_LiveOnly`.

## Run

```bash
LOAD_TEST=1 go test -timeout=15m -run TestLoad_SustainedInsert -count=1 -v .

LOAD_TEST=1 LOAD_TEST_DURATION=10m LOAD_TEST_MAC_COUNT=500 LOAD_TEST_MAC_INTERVAL=2s \
  go test -timeout=25m -run TestLoad_SustainedInsert -count=1 -v .
```

```bash
make loadtest
make loadtest-long
```

## Progress and statistics

While the test runs, progress is logged every **`LOAD_TEST_PROGRESS_INTERVAL`** (default **5s**) to stdout via `t.Log`:

```
ts=2026-06-03T10:49:49+02:00 load  41.7% [==========--------------] elapsed=25s/60s sent=12450 ok=12450 ...
LOAD_PROGRESS ts=2026-06-03T10:49:49+02:00 label=load elapsed_sec=25.00 pct=41.7 sent_rps=498.00 ok_rps=498.00 queue=12 ...
```

Human line includes wall-clock **`ts=`**; **`LOAD_PROGRESS`** is key=value for scripts (`scripts/loadtest_plot.py`).

At the end: `final 100.0% …` summary, then `drain ok: …`.

Set `LOAD_TEST_PROGRESS_INTERVAL=1s` for more frequent updates.

## Environment

| Variable | Default | Purpose |
|----------|---------|---------|
| `LOAD_TEST` | *(unset)* | Must be `1` to run |
| `LOAD_TEST_DURATION` | `60s` | How long sensors emit |
| `LOAD_TEST_MAC_COUNT` | `300` | Number of simulated devices |
| `LOAD_TEST_RPS` | *(unset)* | Target total msg/s (overrides default interval) |
| `LOAD_TEST_MAC_INTERVAL` | `1s` | Per-MAC period (mutually exclusive with `LOAD_TEST_RPS`) |
| `LOAD_TEST_INSERT_INTO` | `INSERT INTO air_readings (...)` | SQL prefix before `VALUES` |
| `LOAD_TEST_JOURNAL` | `false` | Enable journal in fixture |
| `LOAD_TEST_FLUSH_COUNT` | `100` | Bulk batch size |
| `LOAD_TEST_DRAIN_SEC` | `120` | Queue drain timeout after load |
| `LOAD_TEST_LIVE_ONLY` | *(unset)* | Set `1` to disable backup target (default is dual-write) |
| `LOAD_TEST_PROGRESS_INTERVAL` | `5s` | Progress/stats log interval during load |
| `LOAD_TEST_SYNC` | `false` | Wait for HTTP before next tick per MAC |
| `LOAD_TEST_MAX_INFLIGHT` | `0` | Max concurrent HTTP requests (`0` = unlimited). With `LOAD_TEST_JOURNAL=true` at high RPS, set ~`500` to avoid client timeouts. |
| `LOAD_TEST_HTTP_TIMEOUT` | `max(120s, 10×duration)` | Per-request HTTP client timeout |
| `LOAD_TEST_CH_DELAY` | `0` | Mock ClickHouse response delay (live + backup) |
| `LOAD_TEST_CH_DELAY_LIVE` | *(inherits `CH_DELAY`)* | Live mock delay override |
| `LOAD_TEST_CH_DELAY_BACKUP` | *(inherits `CH_DELAY`)* | Backup mock delay override |
| `LOAD_TEST_CH_DELAY_JITTER` | `0` | Random extra delay `0..jitter` per CH request |
| `LOAD_TEST_MEMSTATS` | `false` | Log heap/goroutines/cpu in progress (`true` when `PROFILE=1`) |
| `LOAD_TEST_PROFILE` | `false` | Write `cpu.prof` + heap profiles (also enables memstats) |
| `LOAD_TEST_PROFILE_DIR` | *(temp dir)* | Output directory for pprof files (also enables profiling) |
| `LOAD_TEST_CH_DOWN` | `never` | Mock outage: `never`, `always`, `window` |
| `LOAD_TEST_CH_DOWN_AFTER` | `0` | Outage starts after this from test start |
| `LOAD_TEST_CH_DOWN_FOR` | `0` | Outage length (`0` = until end for `always`) |
| `LOAD_TEST_CH_DOWN_STATUS` | `503` | HTTP status while mock is down |
| `LOAD_TEST_CH_DOWN_LIVE` / `_BACKUP` | *(inherit)* | Per-target outage mode |
| `LOAD_TEST_EXPECT_CH` | auto | `0`/`1` force all targets; default: per-target from outage (`backup always down` → no backup batch check) |
| `LOAD_TEST_CH_CONNECT_TIMEOUT` | `60` | Mock sender connect timeout (seconds) |
| `LOAD_TEST_CH_DOWN_TIMEOUT` | `10` | Mock sender down cooldown (seconds) |

## Pass criteria

- Error rate (network + non-200) **&lt; 1%**
- Send queue drained; mock ClickHouse received batches when `expect_ch=true` (not during full `CH_DOWN=always` unless `LOAD_TEST_EXPECT_CH=1`)
