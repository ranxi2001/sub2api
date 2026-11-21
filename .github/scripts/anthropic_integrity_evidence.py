"""Validate actual business failures/passes, never compilation or skipped tests."""
import json
import pathlib
import re
import sys


def validate(mode, root):
    raw = (root / f'{mode}.jsonl').read_text()
    stderr = (root / f'{mode}.stderr').read_text()
    events = [json.loads(line) for line in raw.splitlines() if line.startswith('{')]
    output = ''.join(event.get('Output', '') for event in events) + stderr
    (root / f'{mode}.log').write_text(output)
    assert not re.search(r'WARNING: DATA RACE|build failed|setup failed|no tests to run|panic:|fatal error:|test timed out', output), output[-8000:]
    assert not any(event.get('Action') == 'skip' for event in events), 'skip contamination'
    outcomes = {event['Test']: event['Action'] for event in events if event.get('Test') and event.get('Action') in ('pass', 'fail')}
    expected = {}
    for route in ('chat', 'responses'):
        for case in ('canonical', 'id', 'comment', 'multidata'):
            expected[f'TestIssue985Framing/{route}/{case}'] = 'fail' if mode == 'red' and case != 'canonical' else 'pass'
        for case in ('empty', 'terminal_only', 'missing_usage', 'stop_reason_error'):
            expected[f'TestIssue985Failure/{route}/{case}'] = 'fail' if mode == 'red' else 'pass'
    for test, outcome in expected.items():
        assert outcomes.get(test) == outcome, (test, outcome, outcomes.get(test))
    rc = int((root / f'{mode}.exitcode').read_text())
    assert rc == (1 if mode == 'red' else 0), rc
    if mode == 'red':
        assert output.count('legal SSE must preserve input 17') == 6, 'RED must fail at business 17/3 oracle'
        assert output.count('incomplete or errored stream must fail') == 8, 'RED must demonstrate false success'
    else:
        assert all(action == 'pass' for action in outcomes.values()), outcomes
    print(f'{mode}: validated {len(expected)} required business cases; exit={rc}')


if __name__ == '__main__':
    validate(sys.argv[1], pathlib.Path(sys.argv[2]))
