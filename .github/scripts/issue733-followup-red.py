"""Require actual review counterexamples; infrastructure errors are not RED."""
import json
import sys

report = json.load(open(sys.argv[1]))
tests = [test for suite in report['testResults'] for test in suite['assertionResults']]
expected = {
    'followup F1 offers Composite': 'real Select option Composite group (#23)',
    'followup F2 fences a complete old R1 pair': 'complete old R1 pair must not publish across successful save',
    'followup F3 aborts the real analysis transport on scope': 'AbortSignal',
    'followup F3 aborts the real analysis transport on logout': 'AbortSignal',
    'followup F3 aborts the real analysis transport on unmount': 'AbortSignal',
}
for name, message in expected.items():
    matched = [test for test in tests if name in test['fullName']]
    assert len(matched) == 1, (name, 'missing or duplicate test')
    test = matched[0]
    errors = '\n'.join(test['failureMessages'])
    assert test['status'] == 'failed' and message in errors, (name, test)
    assert 'AssertionError' in errors and 'ReferenceError' not in errors, (name, errors)
    print('Confirmed business RED:', name)
