"""Negative controls for the strict remote evidence validator (no Go needed)."""
import importlib.util
import json
import pathlib
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('validator', pathlib.Path(__file__).with_name('anthropic_integrity_evidence.py'))
validator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(validator)

class EvidenceControls(unittest.TestCase):
    def exercise(self, mutate=None):
        events = [{'Test': test, 'Action': 'pass'} for test in validator.required_tests('green')]
        if mutate:
            mutate(events)
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            (root / 'green.jsonl').write_text('\n'.join(json.dumps(e) for e in events))
            (root / 'green.stderr').write_text('')
            (root / 'green.exitcode').write_text('0')
            validator.validate('green', root)

    def test_complete(self):
        self.exercise()

    def test_missing(self):
        with self.assertRaises(AssertionError):
            self.exercise(lambda rows: rows.pop())

    def test_pollution(self):
        for text in ['panic: injected', 'http: panic serving fixture', 'runtime error: injected', 'SIGSEGV', 'WARNING: DATA RACE', 'build failed', 'fatal error: crash', 'no tests to run', 'test timed out']:
            with self.subTest(text=text), self.assertRaises(AssertionError):
                self.exercise(lambda rows: rows.append({'Output': text}))

    def test_skipped(self):
        with self.assertRaises(AssertionError):
            self.exercise(lambda rows: rows.append({'Action': 'skip', 'Test': 'extra'}))

    def test_corrupt_output(self):
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            (root / 'green.jsonl').write_text('panic: injected outside JSON\n')
            (root / 'green.stderr').write_text('')
            (root / 'green.exitcode').write_text('0')
            with self.assertRaises(AssertionError):
                validator.validate('green', root)

    def test_nonzero(self):
        with self.assertRaises(AssertionError):
            self.exercise(lambda rows: rows.append({'Test': 'extra', 'Action': 'fail'}))

if __name__ == '__main__':
    unittest.main()
