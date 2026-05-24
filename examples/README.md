# Client examples

Runnable samples for [CLIENT_COMPATIBILITY.md](../docs/CLIENT_COMPATIBILITY.md).

| File | Target | Use case |
|------|--------|----------|
| [go_direct_ch.go](./go_direct_ch.go) | ClickHouse `:9000` (native) | Go apps with clickhouse-go — **bypass bulk** |
| [python_raw_insert.py](./python_raw_insert.py) | bulk `:8124` | High-volume TSV fire-and-forget via `raw_insert` |

## Go — direct ClickHouse (recommended for clickhouse-go)

Requires [clickhouse-go v2](https://github.com/ClickHouse/clickhouse-go).

```bash
cd examples
go run go_direct_ch.go
```

Environment (optional): `CH_ADDR`, `CH_DATABASE`, `CH_USER`, `CH_PASSWORD`.

## Python — raw INSERT through bulk

Requires [clickhouse-connect](https://clickhouse.com/docs/integrations/python):

```bash
pip install clickhouse-connect
export BULK_HOST=127.0.0.1 BULK_PORT=8124 CH_DATABASE=default
python python_raw_insert.py
```

Bulk returns **empty HTTP 200** when the row is accepted (async). Check `/metrics` or ClickHouse for delivery. For synchronous errors, send header `X-Bulk-Sync: 1` (see [CONFIG.md](../docs/CONFIG.md#top-level)).

## curl — batched TabSeparated (no deps)

```bash
curl -sS -X POST 'http://127.0.0.1:8124/' \
  --data-binary $'INSERT INTO db.events FORMAT TabSeparated\n1\thello\n'
```
