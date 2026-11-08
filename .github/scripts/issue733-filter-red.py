"""Require a real missing-filter assertion, never an import or fixture failure."""
import json
import sys

with open(sys.argv[1]) as source:
    report = json.load(source)
assert report.get('numRuntimeErrorTestSuites', 0) == 0, report
tests = [test for suite in report['testResults'] for test in suite['assertionResults']
         if test['title'] == 'offers explicit model and group filters independently of the latest pool']
assert len(tests) == 1 and tests[0]['status'] == 'failed', tests
message = '\n'.join(tests[0]['failureMessages'])
assert 'AssertionError' in message and 'administrators must be able to select the analysis model' in message, message
print('Verified behavioral RED: administrator model filter is missing')
