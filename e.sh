#!/usr/bin/env bash
set -euo pipefail

EXAMPLES_DIR="${1:-examples}"

for dir in "$EXAMPLES_DIR"/*/; do
    [[ -d "$dir" ]] || continue

    name="$(basename "$dir")"

    # Only flatten directories containing exactly one file.
    mapfile -t files < <(find "$dir" -maxdepth 1 -type f -print)

    if [[ "${#files[@]}" -ne 1 ]]; then
        echo "Skipping $dir: expected exactly 1 file, found ${#files[@]}" >&2
        continue
    fi

    src="${files[0]}"
    ext="${src##*.}"
    dst="$EXAMPLES_DIR/simp_${name}.${ext}"

    if [[ -e "$dst" ]]; then
        echo "ERROR: destination already exists: $dst" >&2
        exit 1
    fi

    echo "git mv '$src' '$dst'"
    git mv "$src" "$dst"

    rmdir "$dir"
done
