#!/usr/bin/env bash
# Reject contaminated evidence explicitly; callers must retain their exit-code
# and business-oracle assertions. A historical race RED alone permits race output.
set -euo pipefail
if [ "$#" -lt 2 ]; then
  echo 'usage: assert-test-log-clean.sh green|red|race-red LOG...' >&2
  exit 2
fi
mode="$1"
shift
pattern='build failed|setup failed|undefined:|syntax error:|no tests to run|\[no test files\]|--- SKIP:|panic:|fatal error:|signal:|SIG(SEGV|ABRT|BUS|ILL|FPE|KILL)|Segmentation fault|Aborted|core dumped|test timed out|Failed to resolve|Transform failed|No test files found'
case "$mode" in
  green) pattern+='|WARNING: DATA RACE|race detected during execution of test|--- FAIL:|^FAIL([[:space:]]|$)' ;;
  red) pattern+='|WARNING: DATA RACE|race detected during execution of test' ;;
  race-red) ;;
  *) echo "unknown test-log mode: $mode" >&2; exit 2 ;;
esac
for log in "$@"; do
  if [ ! -f "$log" ] || [ ! -r "$log" ] || [ ! -s "$log" ]; then
    echo "missing, unreadable or empty test log: $log" >&2
    exit 2
  fi
  if grep -niE -- "$pattern" "$log"; then
    echo "rejected contaminated $mode evidence: $log" >&2
    exit 1
  else
    status=$?
    if [ "$status" -ne 1 ]; then
      echo "could not inspect test log: $log (grep exit $status)" >&2
      exit "$status"
    fi
  fi
done
