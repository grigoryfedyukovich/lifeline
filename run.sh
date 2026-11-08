#!/usr/bin/env bash
set -u

LIFELINE="${LIFELINE:-./bin/lifeline}"
EXAMPLES_DIR="${1:-./examples}"

if [ ! -x "$LIFELINE" ]; then
    echo "error: lifeline binary not found or not executable: $LIFELINE" >&2
    exit 1
fi

if [ ! -d "$EXAMPLES_DIR" ]; then
    echo "error: examples directory not found: $EXAMPLES_DIR" >&2
    exit 1
fi

status=0

for dir in "$EXAMPLES_DIR"/*/; do
    [ -d "$dir" ] || continue

    name=$(basename "$dir")
    config="$dir/lifeline.yaml"

    # Run the package/directory. If a benchmark has a local config, supply it.
    if [ -f "$config" ]; then
        echo "run $dir (config: $config)"
        "$LIFELINE" -config "$config" "$dir" || status=$?
    else
        echo "run $dir"
        "$LIFELINE" "$dir" || status=$?
    fi

    echo

done

exit "$status"
