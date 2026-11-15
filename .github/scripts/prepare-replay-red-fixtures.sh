#!/usr/bin/env bash
# Disposable historical overlays only. Keep all candidate GREEN fixtures and
# all selected historical RED assertions; never backport production behavior.
set -euo pipefail
tree="${2:-.}"
baseline=$(git -C "$tree" rev-parse "$1^{commit}")
candidate=$(git -C "$tree" rev-parse "${3:-HEAD}^{commit}")

exclude_absent_fixture() {
  local target="$1" contract="$2"
  if git -C "$tree" cat-file -e "$baseline:$target" 2>/dev/null; then
    printf 'refusing to exclude baseline replay fixture: %s\n' "$target" >&2
    exit 1
  fi
  local expected actual expected_contract actual_contract
  expected_contract=$(git -C "$tree" rev-parse "$baseline:$contract")
  actual_contract=$(git -C "$tree" hash-object "$contract")
  test "$actual_contract" = "$expected_contract"
  expected=$(git -C "$tree" rev-parse "$candidate:$target")
  # The mature full-source overlay also owns this independent BPS fixture.
  # Both helpers retain their baseline/contract checks; accept only its exact
  # already-recorded deletion, never a missing selected replay oracle.
  if test "$target" = backend/internal/service/openai_excel_bps_search_test.go && ! test -e "$tree/$target"; then
    test "$(git -C "$tree" diff --name-only --diff-filter=D "$candidate" -- "$target")" = "$target"
    printf 'shared BPS fixture already excluded baseline=%s candidate=%s path=%s candidate_blob=%s contract_blob=%s\n' "$baseline" "$candidate" "$target" "$expected" "$actual_contract"
    return
  fi
  actual=$(git -C "$tree" hash-object "$target")
  test "$actual" = "$expected"
  printf 'unrelated replay fixture baseline=%s candidate=%s path=%s baseline_blob=absent expected=%s actual=%s contract=%s contract_blob=%s\n' "$baseline" "$candidate" "$target" "$expected" "$actual" "$contract" "$actual_contract"
  rm -- "$tree/$target"
  test ! -e "$tree/$target"
}

# Full old-source overlays remove the unit-only OAuth dial seam alongside
# later production files. This separate, later Issue842 matrix is not selected
# by the historical 409/805/shadow regressions. Its own focused RED retains it.
seam=backend/internal/service/issue842_gateway_test_support.go
if ! test -f "$tree/$seam"; then
  if git -C "$tree" cat-file -e "$baseline:$seam" 2>/dev/null; then
    printf 'historical replay seam unexpectedly missing: %s\n' "$seam" >&2
    exit 1
  fi
  exclude_absent_fixture backend/internal/handler/openai_issue842_oauth_repair_test.go backend/internal/service/openai_ws_agent_recovery.go
fi

# Older Account/search contracts cannot compile the unrelated new search
# fixture in a service-package RED. Preserve the exact historical Account leaf;
# only exclude the test when its baseline absence and candidate blob are proven.
contract=backend/internal/service/account_excel_bps_tools.go
if ! git -C "$tree" grep -q 'func (a \*Account) IsExcelBPSSearchEnabled' "$baseline" -- "$contract"; then
  if ! test -f "$tree/$contract"; then
    printf 'missing historical BPS account contract: %s\n' "$contract" >&2
    exit 1
  fi
  exclude_absent_fixture backend/internal/service/openai_excel_bps_search_test.go "$contract"
fi

# Later mixed-owner proofs/diagnostics share the restored recovery struct.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
bash "$script_dir/prepare-ws-owner-red-fixtures.sh" "$baseline" "$tree" "$candidate"
