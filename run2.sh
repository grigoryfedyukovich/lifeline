#!/usr/bin/env bash
set -u

LIFELINE="${LIFELINE:-./bin/lifeline}"
EXAMPLES_DIR="${1:-./examples}"
OUT="${2:-baseline-$(date +%Y%m%d-%H%M%S).txt}"

: > "$OUT"

for dir in "$EXAMPLES_DIR"/*/; do
    [ -d "$dir" ] || continue

    name="$(basename "$dir")"

    echo
    echo "===== $name =====" | tee -a "$OUT"

    args=("$dir")

    if [ -f "$dir/lifeline.yaml" ]; then
        args=(-config "$dir/lifeline.yaml" "$dir")
    fi

    echo "+ $LIFELINE ${args[*]}" | tee -a "$OUT"
    "$LIFELINE" "${args[@]}" 2>&1 | tee -a "$OUT"

    status=${PIPESTATUS[0]}
    echo "[exit $status]" | tee -a "$OUT"
done

echo
echo "Results written to: $OUT"
