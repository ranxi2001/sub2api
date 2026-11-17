#!/usr/bin/env bash
# The historical recovery overlay predates usage-range sorting. Its unchanged
# candidate-only handler test cannot compile against that old API. GREEN keeps
# the fixture; only an exact, disposable historical API permits its omission.
set -euo pipefail
baseline="$1"
tree="${2:-.}"
candidate="${3:-HEAD}"
target=backend/internal/handler/api_key_handler_usage_sort_test.go
contract=backend/internal/service/api_key.go
if ! git -C "$tree" cat-file -e "$candidate:$target" 2>/dev/null; then exit 0; fi
if git -C "$tree" cat-file -e "$baseline:$target" 2>/dev/null ||
   git -C "$tree" grep -q 'UsageStartTime' "$baseline" -- "$contract"; then
  echo 'refusing to omit a usage fixture supported by this baseline' >&2
  exit 1
fi
expected_contract=$(git -C "$tree" rev-parse "$baseline:$contract")
test "$(git -C "$tree" hash-object "$contract")" = "$expected_contract"
expected=$(git -C "$tree" rev-parse "$candidate:$target")
if ! test -e "$tree/$target"; then
  test "$(git -C "$tree" diff --name-only --diff-filter=D "$candidate" -- "$target")" = "$target"
  printf 'usage-sort fixture already excluded baseline=%s candidate=%s blob=%s contract=%s path=%s\n' "$baseline" "$candidate" "$expected" "$expected_contract" "$target"
  exit 0
fi
test "$(git -C "$tree" hash-object "$target")" = "$expected"
rm -- "$tree/$target"
test ! -e "$tree/$target"
printf 'independent usage-sort fixture baseline=%s candidate=%s blob=%s contract=%s path=%s actual=absent\n' "$baseline" "$candidate" "$expected" "$expected_contract" "$target"
