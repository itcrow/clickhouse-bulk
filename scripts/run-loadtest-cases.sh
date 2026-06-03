#!/usr/bin/env bash
# Run load-test scenarios from the Makefile, collect logs, plot LOAD_PROGRESS metrics.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-$ROOT/loadtest-results/$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUT"

# name:make-target (default: 2×2h journal on/off; LOADTEST_SUITE_MODE=quick for short cases)
if [[ "${LOADTEST_SUITE_MODE:-long}" == "quick" ]]; then
	DEFAULT_CASES="
baseline:loadtest-short
journal:loadtest-short-journal
ch-delay:loadtest-short-ch-delay
ch-down-window:loadtest-ch-down-window
ch-down-always:loadtest-ch-down-always
ch-down-backup:loadtest-ch-down-backup
"
else
	DEFAULT_CASES="
no-journal:loadtest-suite-no-journal
journal:loadtest-suite-journal
"
fi

CASES="${LOADTEST_SUITE_CASES:-$DEFAULT_CASES}"

echo "Load test suite → $OUT (mode=${LOADTEST_SUITE_MODE:-long}, cases=$(echo "$CASES" | grep -c ':'))" | tee "$OUT/run.log"

summary="$OUT/summary.tsv"
echo -e "case\texit\tlog" > "$summary"

while IFS= read -r entry; do
	[[ -z "${entry// /}" ]] && continue
	name="${entry%%:*}"
	target="${entry#*:}"
	log="$OUT/${name}.log"
	echo "==> case=$name make $target" | tee -a "$OUT/run.log"
	set +e
	make -C "$ROOT" "$target" > >(tee "$log") 2>&1
	rc=$?
	set -e
	echo -e "${name}\t${rc}\t${log}" >> "$summary"
done <<< "$CASES"

python3 "$ROOT/scripts/loadtest_plot.py" "$OUT"
echo "Results: $OUT"
echo "Charts:  $OUT/charts"
