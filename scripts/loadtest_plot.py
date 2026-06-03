#!/usr/bin/env python3
"""Parse LOAD_PROGRESS lines from load test logs and render comparison charts."""

from __future__ import annotations

import csv
import re
import sys
from pathlib import Path

try:
	import matplotlib.pyplot as plt
except ImportError as exc:  # pragma: no cover
	print("matplotlib required: pip install matplotlib", file=sys.stderr)
	raise SystemExit(1) from exc

PROGRESS_RE = re.compile(
	r"LOAD_PROGRESS\s+"
	r"ts=(?P<ts>\S+)\s+"
	r"label=(?P<label>\S+)\s+"
	r"elapsed_sec=(?P<elapsed_sec>[-\d.]+)\s+"
	r"pct=(?P<pct>[-\d.]+)\s+"
	r"sent=(?P<sent>\d+)\s+"
	r"ok=(?P<ok>\d+)\s+"
	r"err=(?P<err>\d+)\s+"
	r"non200=(?P<non200>\d+)\s+"
	r"sent_rps=(?P<sent_rps>[-\d.]+)\s+"
	r"ok_rps=(?P<ok_rps>[-\d.]+)\s+"
	r"sent_window=(?P<sent_window>[-\d.]+)\s+"
	r"ok_window=(?P<ok_window>[-\d.]+)\s+"
	r"target_rps=(?P<target_rps>[-\d.]+)\s+"
	r"inflight=(?P<inflight>\d+)\s+"
	r"queue=(?P<queue>\d+)\s+"
	r"live_batches=(?P<live_batches>\d+)\s+"
	r"backup_batches=(?P<backup_batches>\d+)\s+"
	r"max_lat_ms=(?P<max_lat_ms>[-\d.]+)"
	r"(?:\s+heap_alloc=(?P<heap_alloc>\d+)\s+heap_inuse=(?P<heap_inuse>\d+)\s+sys=(?P<sys>\d+)\s+goroutines=(?P<goroutines>\d+)\s+num_gc=(?P<num_gc>\d+)\s+cpu_cores=(?P<cpu_cores>[-\d.]+))?"
)


def parse_log(path: Path) -> list[dict[str, str]]:
	rows: list[dict[str, str]] = []
	for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
		m = PROGRESS_RE.search(line)
		if not m or m.group("label") != "load":
			continue
		rows.append(m.groupdict())
	return rows


def to_float(row: dict[str, str], key: str) -> float:
	val = row.get(key)
	return float(val) if val not in (None, "") else 0.0


def plot_case(case: str, rows: list[dict[str, str]], out_dir: Path) -> None:
	if not rows:
		return
	x = [to_float(r, "elapsed_sec") for r in rows]
	fig, axes = plt.subplots(2, 2, figsize=(12, 8))
	fig.suptitle(f"load test: {case}")

	axes[0, 0].plot(x, [to_float(r, "sent_rps") for r in rows], label="sent_rps")
	axes[0, 0].plot(x, [to_float(r, "ok_rps") for r in rows], label="ok_rps")
	if rows:
		axes[0, 0].axhline(to_float(rows[0], "target_rps"), color="gray", ls="--", label="target_rps")
	axes[0, 0].set_title("RPS")
	axes[0, 0].set_xlabel("elapsed_sec")
	axes[0, 0].legend()
	axes[0, 0].grid(True, alpha=0.3)

	axes[0, 1].plot(x, [to_float(r, "queue") for r in rows], label="queue", color="tab:red")
	axes[0, 1].plot(x, [to_float(r, "inflight") for r in rows], label="inflight", color="tab:orange")
	axes[0, 1].set_title("Backlog")
	axes[0, 1].set_xlabel("elapsed_sec")
	axes[0, 1].legend()
	axes[0, 1].grid(True, alpha=0.3)

	axes[1, 0].plot(x, [to_float(r, "max_lat_ms") for r in rows], label="max_lat_ms")
	axes[1, 0].set_title("Client max latency (ms)")
	axes[1, 0].set_xlabel("elapsed_sec")
	axes[1, 0].grid(True, alpha=0.3)

	axes[1, 1].plot(x, [to_float(r, "live_batches") for r in rows], label="live_batches")
	axes[1, 1].plot(x, [to_float(r, "backup_batches") for r in rows], label="backup_batches")
	axes[1, 1].set_title("CH batches received (mock)")
	axes[1, 1].set_xlabel("elapsed_sec")
	axes[1, 1].legend()
	axes[1, 1].grid(True, alpha=0.3)

	fig.tight_layout()
	fig.savefig(out_dir / f"{case}.png", dpi=120)
	plt.close(fig)


def plot_overview(all_cases: dict[str, list[dict[str, str]]], out_dir: Path) -> None:
	fig, axes = plt.subplots(1, 3, figsize=(14, 4))
	fig.suptitle("load test suite overview")

	for case, rows in sorted(all_cases.items()):
		if not rows:
			continue
		x = [to_float(r, "elapsed_sec") for r in rows]
		axes[0].plot(x, [to_float(r, "ok_rps") for r in rows], label=case)
		axes[1].plot(x, [to_float(r, "queue") for r in rows], label=case)
		axes[2].plot(x, [to_float(r, "max_lat_ms") for r in rows], label=case)

	for ax, title in zip(axes, ("ok_rps", "queue", "max_lat_ms")):
		ax.set_title(title)
		ax.set_xlabel("elapsed_sec")
		ax.legend(fontsize=7)
		ax.grid(True, alpha=0.3)

	fig.tight_layout()
	fig.savefig(out_dir / "overview.png", dpi=120)
	plt.close(fig)


def main() -> None:
	if len(sys.argv) != 2:
		print(f"usage: {sys.argv[0]} <results-dir>", file=sys.stderr)
		raise SystemExit(2)

	root = Path(sys.argv[1])
	charts = root / "charts"
	charts.mkdir(parents=True, exist_ok=True)

	all_cases: dict[str, list[dict[str, str]]] = {}
	csv_path = root / "metrics.csv"

	with csv_path.open("w", newline="", encoding="utf-8") as fh:
		writer: csv.DictWriter | None = None
		for log in sorted(root.glob("*.log")):
			case = log.stem
			rows = parse_log(log)
			all_cases[case] = rows
			for row in rows:
				row["case"] = case
				if writer is None:
					writer = csv.DictWriter(fh, fieldnames=["case", *row.keys()])
					writer.writeheader()
				writer.writerow(row)
			plot_case(case, rows, charts)

	plot_overview(all_cases, charts)
	print(f"wrote {csv_path} and charts in {charts}")


if __name__ == "__main__":
	main()
