#!/usr/bin/env bash
# Disposable historical overlays only. Keep all candidate GREEN fixtures and
# all selected historical RED assertions; never backport production behavior.
set -euo pipefail
tree="${2:-.}"
baseline=$(git -C "$tree" rev-parse "$1^{commit}")
candidate=$(git -C "$tree" rev-parse "${3:-HEAD}^{commit}")
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
python3 "$script_dir/prepare-upstream-v0222-red-fixtures.py" "$baseline" "$tree" "$candidate"

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

# Full historical overlays restore the old APIKeyListFilters as well. Preserve
# the later usage tests in selective overlays and every candidate GREEN; only
# the exact old contract can justify excluding this independent fixture.
usage_contract=backend/internal/service/api_key.go
if ! git -C "$tree" grep -q 'UsageStartTime' "$baseline" -- "$usage_contract" &&
   test "$(git -C "$tree" hash-object "$usage_contract")" = "$(git -C "$tree" rev-parse "$baseline:$usage_contract")"; then
  bash "$(dirname "${BASH_SOURCE[0]}")/prepare-usage-sort-red-fixture.sh" "$baseline" "$tree" "$candidate"
fi

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

# GPT-6.2 catalog/pricing tests use APIs absent in older full-source overlays.
# Only the unrelated post-baseline fixture is excluded, with both the original
# production contract and candidate test blob checked by the helper.
contract=backend/internal/service/openai_model_alias.go
target=backend/internal/service/openai_gpt62_test.go
if git -C "$tree" cat-file -e "$candidate:$target" 2>/dev/null &&
   ! git -C "$tree" grep -q 'func normalizeOpenAIGPT62Model' "$baseline" -- "$contract"; then
  exclude_absent_fixture "$target" "$contract"
fi

# PR878 added a Group field used by otherwise unrelated service-package tests.
# A full old-source overlay must compile them against that same historical API;
# selected recovery oracles and all candidate GREEN tests remain untouched.
contract=backend/internal/service/group.go
if ! git -C "$tree" grep -q 'FreeOpenAIUltrafast' "$baseline" -- "$contract"; then
  expected_contract=$(git -C "$tree" rev-parse "$baseline:$contract")
  test "$(git -C "$tree" hash-object "$contract")" = "$expected_contract"
  for target in backend/internal/service/admin_group_duplicate_test.go backend/internal/service/admin_service_group_test.go backend/internal/service/api_key_auth_cache_openai_fast_test.go backend/internal/service/free_ultrafast_customer_cost_test.go; do
    if ! git -C "$tree" cat-file -e "$candidate:$target" 2>/dev/null; then
      continue
    fi
    candidate_blob=$(git -C "$tree" rev-parse "$candidate:$target")
    test "$(git -C "$tree" hash-object "$target")" = "$candidate_blob"
    if git -C "$tree" cat-file -e "$baseline:$target" 2>/dev/null; then
      expected=$(git -C "$tree" rev-parse "$baseline:$target")
      git -C "$tree" show "$baseline:$target" > "$tree/$target"
      test "$(git -C "$tree" hash-object "$target")" = "$expected"
      printf 'historical group fixture path=%s candidate_blob=%s baseline_blob=%s contract_blob=%s\n' "$target" "$candidate_blob" "$expected" "$expected_contract"
    else
      exclude_absent_fixture "$target" "$contract"
    fi
  done
fi

# PR882 expanded TOTP rotation independently of the replay oracle. When the
# entire production tree is historical, use its matching TOTP test as well.
# Candidate GREEN tests and selected replay assertions remain untouched.
contract=backend/internal/service/openai_totp_rotation.go
target=backend/internal/service/openai_totp_rotation_test.go
if git -C "$tree" cat-file -e "$baseline:$contract" 2>/dev/null &&
   ! git -C "$tree" grep -q 'PreviousCiphertext' "$baseline" -- "$contract"; then
  expected_contract=$(git -C "$tree" rev-parse "$baseline:$contract")
  test "$(git -C "$tree" hash-object "$contract")" = "$expected_contract"
  candidate_blob=$(git -C "$tree" rev-parse "$candidate:$target")
  test "$(git -C "$tree" hash-object "$target")" = "$candidate_blob"
  expected=$(git -C "$tree" rev-parse "$baseline:$target")
  git -C "$tree" show "$baseline:$target" > "$tree/$target"
  test "$(git -C "$tree" hash-object "$target")" = "$expected"
  printf 'historical TOTP fixture path=%s candidate_blob=%s baseline_blob=%s contract_blob=%s\n' "$target" "$candidate_blob" "$expected" "$expected_contract"
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
  # PR872 added independent search diagnostics/race fixtures which also use
  # that absent API and the excluded search fixture helper. Keep them in every
  # candidate GREEN; only isolate proven post-baseline files in this old RED.
  for target in backend/internal/service/openai_excel_bps_search_diagnostics_test.go backend/internal/service/openai_excel_bps_search_race_test.go; do
    if git -C "$tree" cat-file -e "$candidate:$target" 2>/dev/null; then
      exclude_absent_fixture "$target" "$contract"
    fi
  done
fi

# Later mixed-owner proofs/diagnostics share the restored recovery struct.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
bash "$script_dir/prepare-ws-owner-red-fixtures.sh" "$baseline" "$tree" "$candidate"

# PR874 creation-only OAuth policy is absent in historical recovery sources.
# Keep the selected recovery tests untouched and all candidate GREEN fixtures;
# adapt only these unrelated service fixtures to the exact old Account API.
contract=backend/internal/service/account.go
if ! git -C "$tree" grep -q 'InitialAPIKeyDisposition' "$baseline" -- "$contract"; then
  target=backend/internal/service/oauth_apikey_disposition_test.go
  if git -C "$tree" cat-file -e "$candidate:$target" 2>/dev/null; then
    exclude_absent_fixture "$target" "$contract"
  fi
  target=backend/internal/service/openai_plugin_account_directory_test.go
  expected_contract=$(git -C "$tree" rev-parse "$baseline:$contract")
  test "$(git -C "$tree" hash-object "$contract")" = "$expected_contract"
  candidate_blob=$(git -C "$tree" rev-parse "$candidate:$target")
  test "$(git -C "$tree" hash-object "$target")" = "$candidate_blob"
  if git -C "$tree" cat-file -e "$baseline:$target" 2>/dev/null; then
    expected=$(git -C "$tree" rev-parse "$baseline:$target")
    git -C "$tree" show "$baseline:$target" > "$tree/$target"
    test "$(git -C "$tree" hash-object "$target")" = "$expected"
    printf 'historical plugin fixture path=%s candidate_blob=%s baseline_blob=%s contract_blob=%s\n' "$target" "$candidate_blob" "$expected" "$expected_contract"
  else
    exclude_absent_fixture "$target" "$contract"
  fi
fi
