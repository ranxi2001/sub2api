#!/usr/bin/env bash
# Remote CI only. Preserve startup crashes without uploading core memory.
set -euo pipefail
label="$1"
checkout="$2"
regression="$3"
timeout="$4"
evidence="$GITHUB_WORKSPACE/evidence"
binary="$RUNNER_TEMP/oauth-degradation-$label.test"
mkdir -p "$evidence"
printf 'cd %q/backend && go test -c -race -tags=integration ./internal/repository -o %q\n' "$checkout" "$binary" >> "$evidence/commands.txt"
printf 'cd %q/backend/internal/repository && %q -test.run %q -test.count=1 -test.v=true -test.timeout=%q\n' "$checkout" "$binary" "$regression" "$timeout" >> "$evidence/commands.txt"
(cd "$checkout/backend" && go test -c -race -tags=integration ./internal/repository -o "$binary") > "$evidence/$label-build.log" 2>&1
ulimit -c unlimited
sudo sysctl -w "kernel.core_pattern=$RUNNER_TEMP/oauth-degradation-$label.core.%p" > "$evidence/$label-core-pattern.txt"
export GOTRACEBACK=crash
set +e
(cd "$checkout/backend/internal/repository" && "$binary" -test.run "$regression" -test.count=1 -test.v=true -test.timeout="$timeout") > "$evidence/$label.log" 2>&1
result=$?
set -e
cat "$evidence/$label.log"
printf '%s\n' "$result" > "$evidence/$label-exit.txt"
if [ "$result" -ne 0 ]; then
  for core in "$RUNNER_TEMP"/oauth-degradation-"$label".core.*; do
    [ -f "$core" ] || continue
    # Core memory and frame arguments can contain the runner environment.
    # Retain only symbols/stacks/mappings, never raw cores, locals or arguments.
    gdb -batch -iex 'set print frame-arguments none' -ex 'info sharedlibrary' \
      -ex 'thread apply all bt' "$binary" "$core" \
      > "$evidence/$label-backtrace-$(basename "$core").txt" 2>&1 || true
  done
fi
exit "$result"
