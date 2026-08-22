#!/usr/bin/env bash
# Verify that docs/en/ and docs/ja/ are full structural mirrors.
#
# Every docs/en/PATH/X.md must have a paired docs/ja/PATH/X.ja.md,
# and vice versa. This enforces the en/ja mandatory mirror rule
# (see CONVENTIONS.md §Documentation structure and the repository's
# docs/ ADRs).
#
# Exit 0 = in sync; exit 1 = drift detected (with diagnostic
# output on stderr listing the unpaired files).
#
# Intended usage:
#   - manual: ./scripts/docs-mirror-check.sh
#   - `make check`
#   - pre-commit hook

set -euo pipefail

# Run from repo root regardless of where the script is invoked.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR/.."

if [ ! -d docs/en ] || [ ! -d docs/ja ]; then
    echo "ERROR: docs/en and docs/ja must both exist." >&2
    exit 1
fi

# Build canonical path keys (relative to docs/{en,ja}/, .ja stripped on ja side).
en_keys=$(find docs/en -type f -name '*.md' | \
    sed -e 's|^docs/en/||' -e 's|\.md$||' | sort)

ja_keys=$(find docs/ja -type f -name '*.md' | \
    sed -e 's|^docs/ja/||' -e 's|\.ja\.md$||' -e 's|\.md$||' | sort)

# comm -23: lines only in left (en files with no ja mirror)
# comm -13: lines only in right (ja files with no en mirror)
missing_in_ja=$(comm -23 <(echo "$en_keys") <(echo "$ja_keys") || true)
missing_in_en=$(comm -13 <(echo "$en_keys") <(echo "$ja_keys") || true)

errors=0

if [ -n "$missing_in_ja" ]; then
    echo "ERROR: docs/en files with no paired Japanese mirror in docs/ja/:" >&2
    echo "$missing_in_ja" | while read -r key; do
        [ -z "$key" ] && continue
        echo "  docs/en/${key}.md  →  expected docs/ja/${key}.ja.md" >&2
    done
    errors=$((errors + 1))
fi

if [ -n "$missing_in_en" ]; then
    echo "ERROR: docs/ja files with no paired English mirror in docs/en/:" >&2
    echo "$missing_in_en" | while read -r key; do
        [ -z "$key" ] && continue
        echo "  docs/ja/${key}.ja.md  →  expected docs/en/${key}.md" >&2
    done
    errors=$((errors + 1))
fi

if [ "$errors" -ne 0 ]; then
    echo "" >&2
    echo "docs/en and docs/ja must be full structural mirrors." >&2
    exit 1
fi

count=$(echo "$en_keys" | wc -l | tr -d ' ')
echo "OK: docs/en and docs/ja are in mirror sync (${count} files)."
