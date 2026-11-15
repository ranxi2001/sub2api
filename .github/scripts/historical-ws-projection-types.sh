#!/usr/bin/env bash
# Disposable historical assemblies only: retain types, strip the unused new
# anchor constructor whose signature/body requires later recovery APIs.
set -euo pipefail
baseline="$1"
bridge_baseline="${2:-$baseline}"
target=backend/internal/service/openai_ws_plaintext_projection.go
bridge=backend/internal/service/openai_ws_http_bridge.go
expected_candidate=e9b345d5055fe055c7891ccb67a93179c49152e2
expected_overlay=88ab6381539ae1620f28807169c7bc3b7d25a220
test "$(git rev-parse "HEAD:$target")" = "$expected_candidate"
test "$(git hash-object "$target")" = "$expected_candidate"
if git cat-file -e "$baseline:$target" 2>/dev/null; then
  echo "refusing to strip a historical baseline helper" >&2
  exit 1
fi
test ! -e backend/internal/service/openai_ws_agent_recovery.go
test "$(git hash-object "$bridge")" = "$(git rev-parse "$bridge_baseline:$bridge")"
# No remaining executable caller may depend on the removed constructor.
set +e
git grep -n 'openAIWSBridgeAnchor' -- 'backend/**/*.go' ':!backend/**/*_test.go' \
  ':!backend/internal/service/openai_ws_plaintext_projection.go'
references=$?
set -e
test "$references" -eq 1
for oracle in backend/internal/handler/openai_issue715_recovery_test.go backend/internal/handler/openai_issue715_delivery_race_test.go; do
  expected=$(git rev-parse "HEAD:$oracle")
  actual=$(git hash-object "$oracle")
  test "$expected" = "$actual"
  printf 'retained oracle path=%s expected=%s actual=%s\n' "$oracle" "$expected" "$actual"
done
sed -i '/^func openAIWSBridgeAnchor(/,/^}$/d' "$target"
actual=$(git hash-object "$target")
test "$actual" = "$expected_overlay"
printf 'historical projection types baseline=%s bridge_baseline=%s path=%s baseline_blob=absent candidate_blob=%s expected_overlay=%s tested_overlay=%s\n' \
  "$baseline" "$bridge_baseline" "$target" "$expected_candidate" "$expected_overlay" "$actual"
