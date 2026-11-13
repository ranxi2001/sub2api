#!/usr/bin/env bash
# Remote CI only. Diagnostics never replace the first race regression result.
set -euo pipefail
label="$1"
checkout="$2"
regression="$3"
evidence="$checkout/evidence"
binary="$RUNNER_TEMP/issue817-$label-$GITHUB_RUN_ID.test"
mkdir -p "$evidence"
go version > "$evidence/toolchain.txt"
go env GOOS GOARCH CGO_ENABLED GOEXPERIMENT >> "$evidence/toolchain.txt"
uname -a > "$evidence/kernel.txt"
ldd --version > "$evidence/libc.txt" 2>&1
printf 'go test -c -race -tags=integration ./internal/repository -o %q\n' "$binary" > "$evidence/command.txt"
printf 'cd %q && %q -test.run %q -test.count=1 -test.v=true -test.timeout=10m\n' "$checkout/backend/internal/repository" "$binary" "$regression" >> "$evidence/command.txt"
if ! (cd "$checkout/backend" && go test -c -race -tags=integration ./internal/repository -o "$binary") > "$evidence/build.log" 2>&1; then
  cat "$evidence/build.log"
  exit 1
fi
go version -m "$binary" > "$evidence/binary-modules.txt"
go tool buildid "$binary" > "$evidence/binary-buildid.txt"
sha256sum "$binary" > "$evidence/binary-before.sha256"
ulimit -c 0
export GOTRACEBACK=crash
set +e
(cd "$checkout/backend/internal/repository" && "$binary" -test.run "$regression" -test.count=1 -test.v=true -test.timeout=10m) > "$evidence/regression.log" 2>&1
result=$?
set -e
cat "$evidence/regression.log"
printf '%s\n' "$result" > "$evidence/regression-exit.txt"
sha256sum "$binary" > "$evidence/binary-after.sha256"
cmp "$evidence/binary-before.sha256" "$evidence/binary-after.sha256"

if [ "$result" -eq 139 ]; then
  # Live first-signal capture avoids trusting the old core/executable mismatch.
  # Keep ASLR enabled as in the uninstrumented invocation. Never print frame
  # arguments, locals, the environment, memory dumps or raw core files.
  set +e
  (cd "$checkout/backend/internal/repository" && timeout --kill-after=5s 90s gdb -nx -batch \
    -iex 'set auto-load off' -iex 'set print frame-arguments none' \
    -ex 'set pagination off' -ex 'set confirm off' \
    -ex 'set disable-randomization off' -ex 'handle SIGSEGV stop print nopass' \
    -ex run -ex 'p $_siginfo' -ex 'info registers pc sp' \
    -ex 'x/8i $pc' -ex 'info sharedlibrary' -ex 'thread apply all bt' \
    --args "$binary" -test.run "$regression" -test.count=1 -test.v=true -test.timeout=60s) > "$evidence/first-signal.txt" 2>&1
  debugger_result=$?
  # This still enters the exact TestMain but selects no product tests. It is
  # a startup comparison only, and cannot satisfy either RED or GREEN.
  (cd "$checkout/backend/internal/repository" && timeout --kill-after=5s 60s "$binary" -test.run '^$' -test.count=1 -test.v=true -test.timeout=45s) > "$evidence/startup-only.log" 2>&1
  startup_result=$?
  set -e
  printf '%s\n' "$debugger_result" > "$evidence/debugger-exit.txt"
  printf '%s\n' "$startup_result" > "$evidence/startup-only-exit.txt"

  # Independent stdlib-only race binary exercises the observed os/exec+timer
  # startup path without importing application code. A pass is not exoneration.
  cat > "$RUNNER_TEMP/issue817-startup.go" <<'GO'
package main

import (
    "context"
    "fmt"
    "os/exec"
    "sync"
    "time"
)

func main() {
    var workers sync.WaitGroup
    for i := 0; i < 4; i++ {
        workers.Add(1)
        go func() { defer workers.Done(); for j := 0; j < 20; j++ { <-time.After(time.Millisecond) } }()
    }
    for _, args := range [][]string{{"info"}, {"image", "inspect", "postgres:18.1-alpine3.23"}} {
        fmt.Println("stdlib startup before docker", args[0])
        ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        err := exec.CommandContext(ctx, "docker", args...).Run()
        cancel()
        fmt.Println("stdlib startup after docker", args[0], "error:", err)
    }
    workers.Wait()
}
GO
  set +e
  go build -race -o "$RUNNER_TEMP/issue817-startup" "$RUNNER_TEMP/issue817-startup.go" > "$evidence/stdlib-build.log" 2>&1
  smoke_build=$?
  if [ "$smoke_build" -eq 0 ]; then
    timeout --kill-after=5s 45s "$RUNNER_TEMP/issue817-startup" > "$evidence/stdlib-startup.log" 2>&1
    printf '%s\n' "$?" > "$evidence/stdlib-startup-exit.txt"
  fi
  set -e
  printf '%s\n' "$smoke_build" > "$evidence/stdlib-build-exit.txt"
  sha256sum "$binary" > "$evidence/binary-after-diagnostics.sha256"
  cmp "$evidence/binary-before.sha256" "$evidence/binary-after-diagnostics.sha256"
fi
printf 'passed\n' > "$evidence/binary-identity-ok.txt"
# Even a successful debugger/startup comparison cannot turn a crash into GREEN.
exit "$result"
