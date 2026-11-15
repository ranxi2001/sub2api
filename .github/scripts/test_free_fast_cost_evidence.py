import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('free-fast-cost-evidence.py')
NAME = 'TestFreeFastCustomerCostSnapshot'
MARKER = 'Free Fast must persist the Standard customer cost snapshot'

class EvidenceTest(unittest.TestCase):
    def check_log(self, mode, events, good):
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / 'events.jsonl'
            log.write_text('\n'.join(json.dumps(e) for e in events))
            result = subprocess.run([sys.executable, str(SCRIPT), mode, str(log), NAME], capture_output=True)
            self.assertEqual(result.returncode == 0, good, result.stderr.decode())

    def test_green(self):
        self.check_log('green', [{'Action': 'pass', 'Test': NAME}], True)

    def test_business_red(self):
        self.check_log('red', [{'Action': 'output', 'Test': NAME, 'Output': MARKER}, {'Action': 'fail', 'Test': NAME}], True)

    def test_reject_bad_evidence(self):
        for output in ['panic:', 'panic_recovered', 'WARNING: DATA RACE', 'no tests to run', 'build failed', 'undefined: symbol']:
            with self.subTest(output=output):
                self.check_log('green', [{'Action': 'output', 'Test': NAME, 'Output': output}, {'Action': 'pass', 'Test': NAME}], False)

    def test_reject_missing_tests_and_skips(self):
        for events in [[{'Action': 'pass'}], [{'Action': 'skip', 'Test': NAME}], [{'Action': 'skip', 'Test': 'Other'}, {'Action': 'pass', 'Test': NAME}]]:
            self.check_log('green', events, False)

    def test_reject_unrelated_red(self):
        self.check_log('red', [{'Action': 'output', 'Test': 'Other', 'Output': MARKER}, {'Action': 'fail', 'Test': NAME}], False)

if __name__ == '__main__':
    unittest.main()
