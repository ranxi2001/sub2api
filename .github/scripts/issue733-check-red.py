"""Accept only the named, behavioral assertion failure, never a broken harness."""
import json
import sys
with open(sys.argv[1]) as source:
    report = json.load(source)
assert report.get('numRuntimeErrorTestSuites', 0) == 0, report
matches = [test for suite in report['testResults'] for test in suite['assertionResults']
           if test['title'] == 'retains latency accounts while a same-scope refresh is pending']
assert len(matches) == 1, matches
result = matches[0]
assert result['status'] == 'failed', result
messages = '\n'.join(result['failureMessages'])
assert 'AssertionError' in messages and 'same-scope refresh must retain readable latency accounts' in messages, messages
print('Verified RED: readable latency accounts disappear during same-scope pending refresh')
