#!/usr/bin/env bash
# Disposable historical RED overlays only. New physical-send timezone guards
# call recovery helpers absent in those baselines. Pin the pre-guard callers;
# never replace the causal recovery implementation or selected test fixtures.
set -euo pipefail
timezone_callers=aa8e596e6d6f3e4648d6beba73b39b240dd5357a
git fetch --no-tags --depth=1 origin "$timezone_callers"
for target in backend/internal/service/openai_request_timezone_egress.go backend/internal/service/openai_ws_client.go; do
  git restore --source="$timezone_callers" -- "$target"
  expected=$(git rev-parse "$timezone_callers:$target")
  actual=$(git hash-object "$target")
  test "$expected" = "$actual"
  printf 'timezone compatibility caller baseline=%s path=%s expected=%s actual=%s\n' "$timezone_callers" "$target" "$expected" "$actual"
done
