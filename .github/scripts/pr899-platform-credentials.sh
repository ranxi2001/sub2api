#!/usr/bin/env bash
set -euo pipefail

# Go execution for this acceptance is remote-only by explicit user instruction.
test "${GITHUB_ACTIONS:-}" = true
pr899_root=$(git rev-parse --show-toplevel)
cd "$pr899_root"
test "$(git rev-parse HEAD)" = "$EXPECTED_HEAD_SHA"
pr899_baseline=468099a62adf0fd9852d2c5e024cfa06ff669c38
pr899_red="$RUNNER_TEMP/pr899-platform-baseline"
pr899_evidence="$pr899_root/artifacts/pr899-backend"
mkdir -p "$pr899_evidence"
git worktree add --detach "$pr899_red" "$pr899_baseline"
test "$(git -C "$pr899_red" rev-parse HEAD)" = "$pr899_baseline"

{
  printf 'baseline=%s\ncandidate=%s\n' "$pr899_baseline" "$EXPECTED_HEAD_SHA"
  go version
  printf 'Compatibility overlay: none. Only the two identical test files below are added to the frozen baseline.\n'
} > "$pr899_evidence/identity.txt"
for pr899_file in pr899_independent_platform_test.go pr899_vertex_auth_test.go; do
  cp "backend/internal/service/$pr899_file" "$pr899_red/backend/internal/service/$pr899_file"
  cmp "backend/internal/service/$pr899_file" "$pr899_red/backend/internal/service/$pr899_file"
  sha256sum "backend/internal/service/$pr899_file" >> "$pr899_evidence/identity.txt"
done
git -C "$pr899_red" diff --exit-code
git -C "$pr899_red" status --short >> "$pr899_evidence/identity.txt"

pr899_pattern='^(TestPR899IndependentVertexAPIKeyMustNotConvertToUnsupportedAnthropicAuth|TestPR899VertexServiceAccountJSONRemainsPortable)$'
set +e
(cd "$pr899_red/backend" && go test -tags=unit ./internal/service -run "$pr899_pattern" -count=1 -json) > "$pr899_evidence/red.jsonl" 2> "$pr899_evidence/red.stderr"
pr899_status=$?
set -e
printf '%s\n' "$pr899_status" > "$pr899_evidence/red.exit"
python3 .github/scripts/pr899-assert-oracle.py red "$pr899_evidence/red.jsonl" "$pr899_evidence/red.stderr" "$pr899_status"

set +e
(cd backend && go test -tags=unit ./internal/service -run "$pr899_pattern" -count=1 -json) > "$pr899_evidence/green.jsonl" 2> "$pr899_evidence/green.stderr"
pr899_status=$?
set -e
printf '%s\n' "$pr899_status" > "$pr899_evidence/green.exit"
python3 .github/scripts/pr899-assert-oracle.py green "$pr899_evidence/green.jsonl" "$pr899_evidence/green.stderr" "$pr899_status"

# Includes explicit/inherited/malformed auth modes, zero account/group/model
# writes, same-platform key preservation, and the original platform regressions.
set +e
(cd backend && go test -tags=unit ./internal/service -run '^(TestPR899|TestUpdateAccountPlatform)' -count=1 -json) > "$pr899_evidence/matrix.jsonl" 2> "$pr899_evidence/matrix.stderr"
pr899_status=$?
set -e
printf '%s\n' "$pr899_status" > "$pr899_evidence/matrix.exit"
test "$pr899_status" -eq 0
