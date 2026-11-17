#!/usr/bin/env bash
# Isolate only the later independent Free Fast service tests when an exact
# historical production overlay lacks their private snapshot implementation.
set -euo pipefail
baseline="$1"
tree="${2:-.}"
candidate="${3:-HEAD}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
python3 "$script_dir/prepare-upstream-v0222-red-fixtures.py" "$baseline" "$tree" "$candidate"
if test -f "$tree/backend/internal/service/customer_cost_details.go"; then exit 0; fi
if git -C "$tree" cat-file -e "$baseline:backend/internal/service/customer_cost_details.go" 2>/dev/null; then
  echo 'baseline snapshot implementation unexpectedly absent' >&2; exit 1
fi
contract=backend/internal/service/billing_service.go
test "$(git -C "$tree" hash-object "$contract")" = "$(git -C "$tree" rev-parse "$baseline:$contract")"
target=backend/internal/service/free_fast_customer_cost_test.go
if ! git -C "$tree" cat-file -e "$candidate:$target" 2>/dev/null; then exit 0; fi
if git -C "$tree" cat-file -e "$baseline:$target" 2>/dev/null; then
  echo 'refusing to omit a baseline Free Fast fixture' >&2; exit 1
fi
expected=$(git -C "$tree" rev-parse "$candidate:$target")
if test -f "$tree/$target"; then
  test "$(git -C "$tree" hash-object "$target")" = "$expected"
  rm -- "$tree/$target"
else
  test "$(git -C "$tree" diff --name-only --diff-filter=D "$candidate" -- "$target")" = "$target"
fi
printf 'independent free-fast fixture baseline=%s candidate=%s path=%s blob=%s actual=absent\n' "$baseline" "$candidate" "$target" "$expected"
