#!/usr/bin/env bash
# Only for disposable production baselines predating PR835. Its independent
# 400 fixtures reference APIs absent there; all selected 409/WS tests remain.
set -euo pipefail
baseline="$1"
tree="${2:-.}"
for target in backend/internal/handler/openai_400_omit_history_test.go backend/internal/service/openai_400_omit_history_test.go; do
  if ! git -C "$tree" cat-file -e "$baseline:$target" 2>/dev/null; then
    printf 'unrelated later 400 fixture excluded from historical RED: %s blob=' "$target"
    git -C "$tree" hash-object "$target"
    rm -- "$tree/$target"
    test ! -e "$tree/$target"
  fi
done
