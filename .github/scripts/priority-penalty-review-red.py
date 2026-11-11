#!/usr/bin/env python3
"""Run identical review regressions against the complete a426 implementation."""
from pathlib import Path
import os
import re
import subprocess
import sys

BASE = 'a426477ecc8cec1af43b0545467850ea5768a23d'
if len(sys.argv) == 1:
    old = Path(os.environ['RUNNER_TEMP']) / 'priority-review-old'
    subprocess.run(['git', 'worktree', 'add', '--detach', str(old), BASE], check=True)
    for package in ['service', 'handler']:
        file = Path(f'backend/internal/{package}/priority_penalty_review_test.go')
        (old / file).write_bytes(subprocess.check_output(['git', 'show', 'd79c1044914e7924c38cc4a239e42ff8980698e6:' + str(file)]))
    print(f'Complete review baseline: {BASE}; historical regression fixtures pinned to d79c104.')
else:
    raw = Path(sys.argv[1]).read_text()
    assert not re.search(r'build failed|undefined:|syntax error|setup failed|panic:', raw, re.I), 'not a behavioral red result'
    assert 'precondition: first supplier failure must enter second account acquisition' not in raw, 'handler did not reach the late-admission seam'
    expected = {
        'NativeLease': 'native lease wait must recheck stop before generation write',
        'LateSelection': 'late stop must retain dedicated temporary-unavailable cause',
        'HandlerFailover': 'late policy rejection must replace previous supplier error',
    }
    failed = set(re.findall(r'^--- FAIL: TestPriorityPenaltyReview(\w+)\b', raw, re.M))
    assert failed == set(expected), f'unexpected red tests: {failed}'
    for name, message in expected.items():
        assert message in raw, f'{name} failed for a different reason'
        print(f'Confirmed exact a426 behavior: {name}')
