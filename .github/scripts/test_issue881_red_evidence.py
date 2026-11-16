import importlib.util
from pathlib import Path
import unittest

spec=importlib.util.spec_from_file_location('evidence',Path(__file__).with_name('issue881-red-evidence.py'))
evidence=importlib.util.module_from_spec(spec);spec.loader.exec_module(evidence)
P=evidence.PARENT

def valid():
    return '\n'.join([f'=== RUN   {P}', *[f'=== RUN   {P}/{name}\n    Test: {P}/{name}\n    Messages: recovery loser must conflict' for name in ['existing','concurrent-insert']],
                      f'--- FAIL: {P} (0.1s)',f'    --- FAIL: {P}/existing (0.0s)',f'    --- FAIL: {P}/concurrent-insert (0.1s)'])

class EvidenceTests(unittest.TestCase):
    def test_accepts_both_owned_behavioral_failures(self):
        self.assertTrue(evidence.validate(valid()))

    def test_rejects_missing_failure_or_borrowed_diagnostic(self):
        for raw in [valid().replace(f'--- FAIL: {P}/existing','--- PASS: '+P+'/existing'),
                    valid().replace('Messages: recovery loser must conflict','Messages: unrelated',1),
                    valid().replace(f'Test: {P}/existing',f'Test: {P}/concurrent-insert')]:
            with self.assertRaises(ValueError): evidence.validate(raw)

    def test_rejects_each_execution_contamination(self):
        for marker in ['[build failed]','[setup failed]','undefined: name','no tests to run','--- SKIP: x','panic: x','WARNING: DATA RACE','race detected','timeout','test timed out','deadline exceeded']:
            with self.subTest(marker=marker), self.assertRaises(ValueError): evidence.validate(valid()+'\n'+marker)

if __name__=='__main__': unittest.main()
