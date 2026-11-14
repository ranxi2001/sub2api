#!/usr/bin/env bash
# Only for disposable historical production overlays. Independent 400 fixtures
# can require newer recovery fields; all selected 409/WS tests remain intact.
set -euo pipefail
tree="${2:-.}"
baseline=$(git -C "$tree" rev-parse "$1^{commit}")
candidate=$(git -C "$tree" rev-parse "${3:-HEAD}^{commit}")
recovery=backend/internal/service/openai_agent_message_recovery.go
for target in backend/internal/handler/openai_400_omit_history_test.go backend/internal/service/openai_400_omit_history_test.go; do
  reason=
  if ! git -C "$tree" cat-file -e "$baseline:$target" 2>/dev/null; then
    reason=absent_in_baseline
  elif [ "$target" = backend/internal/service/openai_400_omit_history_test.go ] &&
       git -C "$tree" grep -q apiInputDigest "$candidate" -- "$target" &&
       ! git -C "$tree" grep -q apiInputDigest "$baseline" -- "$recovery"; then
    # The 400 test exists in this baseline, but its candidate adapter assertions
    # depend on a recovery field introduced later. Never backport that field.
    reason=baseline_lacks_api_input_digest
  else
    continue
  fi
  expected_recovery=$(git -C "$tree" rev-parse "$baseline:$recovery")
  actual_recovery=$(git -C "$tree" hash-object "$recovery")
  test "$expected_recovery" = "$actual_recovery"
  expected=$(git -C "$tree" rev-parse "$candidate:$target")
  actual=$(git -C "$tree" hash-object "$target")
  test "$expected" = "$actual"
  printf 'unrelated 400 fixture exclusion baseline=%s candidate=%s path=%s reason=%s expected=%s actual=%s recovery_blob=%s\n' "$baseline" "$candidate" "$target" "$reason" "$expected" "$actual" "$actual_recovery"
  rm -- "$tree/$target"
  test ! -e "$tree/$target"
done
