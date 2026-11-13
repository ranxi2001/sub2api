#!/usr/bin/env bash
# Remote CI evidence gate. Expected RED failures never excuse forbidden errors.
set -euo pipefail
behavior="$1"
first_result="$2"
log="$3"
test -r "$log"
if grep -Eq 'build failed|setup failed|undefined:|syntax error:|no tests to run|panic:|--- SKIP:|WARNING: DATA RACE|race detected during execution|Segmentation fault|segmentation fault|core dumped' "$log"; then
  echo 'Rejected forbidden error in regression evidence' >&2
  exit 1
fi
for child in never_run previous_completed_run; do
  name="TestIssue817QuotaSkipPreservesPending5xxExecution/$child"
  if [ "$behavior" = red ]; then
    test "$first_result" -ne 0
    grep -F -- "--- FAIL: $name" "$log"
    # Attribute the expected 1/actual 0 probe failure to each child's own
    # execution section, rather than accepting one global diagnostic match.
    awk -v name="$name" '
      $1 == "===" && $2 == "RUN" { selected = ($3 == name) }
      selected && /expected:[[:space:]]*1([[:space:]]|$)/ { expected = 1 }
      selected && /actual[[:space:]]*:[[:space:]]*0([[:space:]]|$)/ { actual = 1 }
      selected && /pending 5xx signal must execute after quota-skipped cron before acknowledgement/ { diagnostic = 1 }
      END { exit !(expected && actual && diagnostic) }
    ' "$log"
  elif [ "$behavior" = green ]; then
    test "$first_result" -eq 0
    grep -F -- "--- PASS: $name" "$log"
  else
    echo 'Unknown evidence behavior' >&2
    exit 1
  fi
done
