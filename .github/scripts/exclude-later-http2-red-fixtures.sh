#!/usr/bin/env bash
# Only a disposable historical production overlay may omit these independent
# PR833 tests. Candidate GREEN retains every test. Never omit a selected 409/WS
# assertion or treat the resulting compilation status as behavioral evidence.
set -euo pipefail
baseline="$1"
tree="${2:-.}"
candidate="${3:-$(git -C "$tree" rev-parse HEAD)}"
# Selective Issue715 overlays retain HTTP2 production/support, so keep its tests.
if test -f "$tree/backend/internal/service/http2_gateway_test_support.go"; then
  exit 0
fi
for target in backend/internal/handler/http2_peer_reset_test.go backend/internal/service/http2_peer_reset_test.go backend/internal/service/http2_peer_reset_adapters_test.go; do
  if git -C "$tree" cat-file -e "$baseline:$target" 2>/dev/null; then
    printf 'refusing to exclude baseline HTTP2 test: %s\n' "$target" >&2
    exit 1
  fi
  expected=$(git -C "$tree" rev-parse "$candidate:$target")
  actual=$(git -C "$tree" hash-object "$target")
  test "$expected" = "$actual"
  printf 'unrelated later HTTP2 fixture baseline=%s candidate=%s path=%s expected=%s actual=%s\n' "$baseline" "$candidate" "$target" "$expected" "$actual"
  rm -- "$tree/$target"
  test ! -e "$tree/$target"
done
