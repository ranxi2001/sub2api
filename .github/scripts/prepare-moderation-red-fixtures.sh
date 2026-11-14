#!/usr/bin/env bash
# Disposable historical source overlays only. Candidate GREEN and the focused
# 112d handler-only RED retain all moderation/recovered-audit tests unchanged.
set -euo pipefail
baseline="$1"
tree="${2:-.}"
candidate="${3:-$(git -C "$tree" rev-parse HEAD)}"
if test -f "$tree/backend/internal/service/content_moderation_upstream_scope.go"; then
  exit 0
fi
production=backend/internal/service/content_moderation.go
expected=$(git -C "$tree" rev-parse "$baseline:$production")
test "$(git -C "$tree" hash-object "$production")" = "$expected"
printf 'moderation fixture overlay baseline=%s candidate=%s production=%s blob=%s\n' "$baseline" "$candidate" "$production" "$expected"

# These exact feature fixtures require the absent selected-upstream contract.
# The new recovered-WS audit suite targets 112d, where the contract exists; it
# must not become a compile blocker in unrelated, older recovery counterexamples.
while IFS= read -r target; do
  if git -C "$tree" cat-file -e "$baseline:$target" 2>/dev/null; then
    printf 'refusing to exclude baseline moderation fixture: %s\n' "$target" >&2
    exit 1
  fi
  expected=$(git -C "$tree" rev-parse "$candidate:$target")
  actual=$(git -C "$tree" hash-object "$target")
  test "$actual" = "$expected"
  printf 'excluded path=%s baseline=absent candidate=%s actual=%s\n' "$target" "$expected" "$actual"
  rm -- "$tree/$target"
  test ! -e "$tree/$target"
done < <(git -C "$tree" ls-files 'backend/internal/handler/moderation_upstream*_test.go' 'backend/internal/handler/admin/moderation_upstream_scope_test.go' 'backend/internal/service/content_moderation_upstream_scope_test.go' 'backend/internal/repository/moderation_upstream_scope*_test.go' 'backend/internal/handler/openai_409_ws_audit_test.go')

# This pre-PR848 fixture retains PR834's actual error observer and all existing
# recovery harness entry points, with the historical eight-argument constructor.
fixture_base=afa7dbd6fccffef950febadc666b9243bba2454d
target=backend/internal/handler/openai_ws_v2_passthrough_cyber_test.go
if ! git -C "$tree" cat-file -e "$fixture_base^{commit}" 2>/dev/null; then
  git -C "$tree" fetch --no-tags --depth=1 origin "$fixture_base"
fi
candidate_blob=$(git -C "$tree" rev-parse "$candidate:$target")
test "$(git -C "$tree" hash-object "$target")" = "$candidate_blob"
expected=$(git -C "$tree" rev-parse "$fixture_base:$target")
git -C "$tree" show "$fixture_base:$target" > "$tree/$target"
actual=$(git -C "$tree" hash-object "$target")
test "$actual" = "$expected"
printf 'compatible path=%s fixture_base=%s candidate=%s expected=%s actual=%s\n' "$target" "$fixture_base" "$candidate_blob" "$expected" "$actual"
