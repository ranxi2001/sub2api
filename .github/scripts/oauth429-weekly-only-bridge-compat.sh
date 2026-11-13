#!/usr/bin/env bash
# Historical RED only. Retained readable-continuation code must compile against
# the old bridge type. This adds storage only: the original bridge never writes
# ClientTools, so it stays nil. No 429 parser, observation, scheduling, forwarding
# or selected test is changed. GREEN never invokes this compatibility patch.
set -euo pipefail
tree=$(git -C "$1" rev-parse --show-toplevel)
baseline="$2"
target=backend/internal/service/openai_ws_http_bridge.go
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
patch="$script_dir/oauth429-weekly-only-bridge-compat.patch"
before=$(git -C "$tree" rev-parse "$baseline:$target")
case "$before" in
  ed1708f3c2ee470d80ab97b377ff610050e383fc) after=efe2dce796b21dcdac9c0fa6268887423fe65780 ;; # weekly RED / expiry
  c91d1b2481d3ca0fae69c35290d196984c0fac4e) after=f18f5c8edd520122bc7c15e382b3e33d148d08f1 ;; # Issue715 pre-WS RED
  *) printf 'unreviewed historical bridge blob: %s\n' "$before" >&2; exit 1 ;;
esac
test "$(git hash-object "$tree/$target")" = "$before"
grep -Fq 'state.ClientTools' "$tree/backend/internal/service/openai_ws_readable_continuation.go"
git -C "$tree" diff --exit-code -- '*_test.go'
printf 'bridge compatibility baseline=%s path=%s original=%s patch=%s\n' "$baseline" "$target" "$before" "$(git hash-object "$patch")"
cat "$patch"
# The insertion is line-exact only because the complete original blob was
# checked above; the complete expected post-patch blob is checked below.
git -C "$tree" apply --unidiff-zero --check "$patch"
git -C "$tree" apply --unidiff-zero "$patch"
actual=$(git hash-object "$tree/$target")
test "$actual" = "$after"
printf 'bridge compatibility expected_post=%s actual_post=%s\n' "$after" "$actual"
git -C "$tree" diff --exit-code -- '*_test.go'
