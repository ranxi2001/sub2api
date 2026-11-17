#!/usr/bin/env bash
# Restore the coherent pre-mixed-owner API only in an existing historical RED
# overlay. Selected historical defect blobs and assertions remain untouched.
set -euo pipefail
baseline="$1"
tree="${2:-.}"
candidate="${3:-HEAD}"
contract=backend/internal/service/openai_agent_message_recovery.go
if grep -q 'requireCompletedOwners' "$tree/$contract"; then
  exit 0 # Candidate GREEN or a baseline that already supports this contract.
fi

for target in \
  backend/internal/service/openai_400_omit_history.go \
  backend/internal/service/openai_409_omit_history.go \
  backend/internal/service/openai_lossy_history_restart.go \
  backend/internal/service/openai_tool_provenance.go \
  backend/internal/repository/gateway_cache_tool_provenance.go \
  backend/internal/service/openai_agent_message_delivery.go \
  backend/internal/service/openai_ws_readable_continuation.go \
  backend/internal/service/openai_409_readable_shadow.go \
  backend/internal/service/openai_agent_message_recovery_error.go \
  backend/internal/service/openai_agent_recovery_diagnostics.go \
  backend/internal/service/openai_agent_replay_metadata.go \
  backend/internal/service/openai_account_scheduler.go \
  backend/internal/service/openai_gateway_request_body.go \
  backend/internal/service/openai_ws_agent_recovery.go \
  backend/internal/service/openai_ws_forwarder_ingress.go \
  backend/internal/service/openai_ws_http_bridge.go \
  backend/internal/service/openai_ws_v2_passthrough_adapter.go \
  backend/internal/handler/openai_agent_message_recovery.go \
  backend/internal/handler/openai_gateway_handler.go; do
  test -f "$tree/$target" || continue
  # Do not replace a selected defect blob or an already verified compatibility
  # blob. Only untouched candidate callers can contain this later dependency.
  expected=$(git -C "$tree" rev-parse "$candidate:$target")
  actual=$(git -C "$tree" hash-object "$target")
  test "$actual" = "$expected" || continue
  if git -C "$tree" cat-file -e "$baseline:$target" 2>/dev/null; then
    git -C "$tree" show "$baseline:$target" > "$tree/$target"
    restored=$(git -C "$tree" hash-object "$target")
    test "$restored" = "$(git -C "$tree" rev-parse "$baseline:$target")"
    printf 'ws-owner caller baseline=%s path=%s candidate=%s restored=%s\n' "$baseline" "$target" "$expected" "$restored"
  else
    rm -- "$tree/$target"
    printf 'ws-owner caller baseline=%s path=%s candidate=%s restored=absent\n' "$baseline" "$target" "$expected"
  fi
done

exclude_new_fixture() {
  local target="$1"
  test -f "$tree/$target" || return 0
  if git -C "$tree" cat-file -e "$baseline:$target" 2>/dev/null; then
    printf 'refusing to remove baseline fixture %s\n' "$target" >&2
    exit 1
  fi
  local expected actual
  expected=$(git -C "$tree" rev-parse "$candidate:$target")
  actual=$(git -C "$tree" hash-object "$target")
  test "$actual" = "$expected"
  rm -- "$tree/$target"
  printf 'unrelated ws-owner fixture baseline=%s path=%s candidate=%s actual=absent\n' "$baseline" "$target" "$expected"
}

# New service tests require new private proof/diagnostic types. Their own RED
# uses handler tests; every candidate GREEN retains them and all negative cases.
exclude_new_fixture backend/internal/service/openai_ws_owner_conflict_test.go
# Issue892's later matrix also imports the OAuth dial seam and 400/842 test
# helpers absent from these historical overlays. It is not a selected oracle
# here; its own RED and every clean candidate GREEN retain both exact files.
exclude_new_fixture backend/internal/handler/openai_issue892_timezone_test.go
exclude_new_fixture backend/internal/service/openai_issue892_timezone_test.go
# Very old Issue805 baselines intentionally exclude the later readable harness.
# Only that dependency absence permits removing its new consumer as well.
if ! test -f "$tree/backend/internal/handler/openai_409_ws_rebuild_test.go"; then
  exclude_new_fixture backend/internal/handler/openai_ws_owner_conflict_test.go
fi
