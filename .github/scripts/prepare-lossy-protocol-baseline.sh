#!/usr/bin/env bash
# A complete pinned production tree plus byte-identical candidate oracles.
# No candidate production code or unrelated candidate fixtures enter this RED.
set -euo pipefail
baseline=$(git rev-parse "${3:-321f11c1a4d23302961c223ac030fd09e3f9e6e8}^{commit}")
candidate=$(git rev-parse "$1^{commit}")
tree="$2"
test ! -e "$tree"
git worktree add --detach "$tree" "$baseline"
printf 'baseline=%s tree=%s candidate=%s\n' "$baseline" "$(git rev-parse "$baseline^{tree}")" "$candidate"
for target in \
  backend/internal/handler/openai_409_http_rebuild_test.go \
  backend/internal/handler/openai_409_ws_rebuild_test.go \
  backend/internal/handler/openai_409_ws_oracle_test.go \
  backend/internal/handler/openai_lossy_protocol_test.go \
  backend/internal/service/openai_lossy_protocol_test.go; do
  git show "$candidate:$target" > "$tree/$target"
  expected=$(git rev-parse "$candidate:$target")
  actual=$(git hash-object "$tree/$target")
  test "$expected" = "$actual"
  printf 'oracle=%s candidate_blob=%s actual=%s\n' "$target" "$expected" "$actual"
done
# Includes every production path and dependency, not just the defect files.
git -C "$tree" diff --exit-code "$baseline" -- . ':!**/*_test.go'
for target in backend/internal/service/openai_lossy_history_restart.go backend/internal/service/openai_agent_recovery_diagnostics.go backend/internal/service/openai_agent_message_recovery.go backend/internal/service/openai_responses_namespace.go; do
  expected=$(git rev-parse "$baseline:$target")
  actual=$(git hash-object "$tree/$target")
  test "$expected" = "$actual"
  printf 'production=%s baseline_blob=%s actual=%s\n' "$target" "$expected" "$actual"
done
