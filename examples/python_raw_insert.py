#!/usr/bin/env python3
"""Fire-and-forget TabSeparated INSERT via clickhouse-bulk (async HTTP 200).

Requires: pip install clickhouse-connect

Bulk batches rows; HTTP 200 means accepted (journal/WAL if enabled), not yet visible
in ClickHouse. Monitor bulk /metrics or CH for delivery. For driver-like errors use
header X-Bulk-Sync: 1 or sync_insert in bulk config (see docs/CONFIG.md).
"""

from __future__ import annotations

import os
import sys

try:
    import clickhouse_connect
except ImportError:
    print("Install: pip install clickhouse-connect", file=sys.stderr)
    sys.exit(1)


def main() -> None:
    host = os.environ.get("BULK_HOST", "127.0.0.1")
    port = int(os.environ.get("BULK_PORT", "8124"))
    database = os.environ.get("CH_DATABASE", "default")
    user = os.environ.get("CH_USER", "default")
    password = os.environ.get("CH_PASSWORD", "")

    client = clickhouse_connect.get_client(
        host=host,
        port=port,
        database=database,
        username=user,
        password=password,
        compress=False,  # bulk accepts plain body; P4.2 handles compression if enabled
    )

    # Proxied query — same as direct ClickHouse (P4.3 headers on bulk)
    rows = client.query("SELECT 1 AS ok").result_rows
    print("bulk proxy query:", rows)

    # Async batched INSERT — empty 200 from bulk, no QuerySummary
    client.raw_insert(
        table="events",
        column_names=["id", "msg"],
        insert_block="101\tfrom-python-raw\n102\tbulk-async\n",
        fmt="TabSeparated",
        compression=None,
    )
    print("raw_insert accepted (async); check ClickHouse or bulk metrics for flush")


if __name__ == "__main__":
    main()
