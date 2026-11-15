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

# Preserve current harness entry points needed by independently compiled newer
# tests. Only adapt the known constructor arity in this disposable RED tree.
# Production stays byte-for-byte historical; selected behavior tests stay intact.
target=backend/internal/handler/openai_ws_v2_passthrough_cyber_test.go
candidate_blob=$(git -C "$tree" rev-parse "$candidate:$target")
test "$(git -C "$tree" hash-object "$target")" = "$candidate_blob"
python3 - "$tree/$target" <<'PY_FIXTURE'
from pathlib import Path
import sys
path = Path(sys.argv[1])
source = path.read_text()
new = "service.NewContentModerationService(settingRepo, moderationRepo, nil, nil, nil, nil, nil, nil, accountRepo)"
old = "service.NewContentModerationService(settingRepo, moderationRepo, nil, nil, nil, nil, nil, nil)"
if source.count(new) != 1:
    raise SystemExit("unexpected moderation fixture constructor")
path.write_text(source.replace(new, old, 1))
PY_FIXTURE
actual=$(git -C "$tree" hash-object "$target")
printf 'compatible test-only constructor path=%s candidate=%s adapted=%s\n' "$target" "$candidate_blob" "$actual"
