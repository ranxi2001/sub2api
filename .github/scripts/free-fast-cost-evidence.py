#!/usr/bin/env python3
"""Reject setup failures, skips and absent tests in remote Go evidence."""
import json
import re
import sys
from pathlib import Path

mode, filename, *required = sys.argv[1:]
raw = Path(filename).read_text()
assert not re.search(r'build failed|undefined:|panic:|panic_recovered|fatal error:|runtime error:|WARNING: DATA RACE|no tests to run', raw, re.I), 'setup, panic, race or missing-tests failure'
events = [json.loads(line) for line in raw.splitlines() if line.startswith('{')]
assert events, 'no Go JSON events'
assert not any(e.get('Action') == 'skip' for e in events), 'skipped tests'
for name in required:
    selected = [e for e in events if e.get('Test') == name]
    assert selected, f'missing test {name}'
    action = 'fail' if mode == 'red' else 'pass'
    assert any(e.get('Action') == action for e in selected), f'{name}: expected {action}'
    if mode == 'red':
        output = ''.join(e.get('Output', '') for e in events
                         if e.get('Test') == name or e.get('Test', '').startswith(name + '/'))
        marker = ('Standard cache prices must be persisted from the resolved quote' if name.endswith('CachePrices')
                  else 'restricted admin must expose the immutable customer Standard quote' if name.endswith('ScopedAdmin')
                  else 'user/admin DTO must expose the immutable customer Standard quote' if name.endswith('DTO')
                  else 'stored usage must retain the customer Standard quote' if name.endswith('Persistence')
                  else 'Free Fast must persist the Standard customer cost snapshot')
        assert marker in output, f'{name}: missing target business assertion'
if mode == 'red':
    assert any(marker in raw for marker in [
        'Free Fast must persist the Standard customer cost snapshot',
        'user/admin DTO must expose the immutable customer Standard quote',
        'restricted admin must expose the immutable customer Standard quote',
        'Standard cache prices must be persisted from the resolved quote',
        'stored usage must retain the customer Standard quote',
    ]), 'RED did not reach a target business assertion'
else:
    assert not any(e.get('Action') == 'fail' for e in events), 'GREEN has failures'
print(f'{mode}: verified {len(required)} required tests; no setup errors, panic, race or skips')
