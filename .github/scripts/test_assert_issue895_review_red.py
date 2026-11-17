"""Remote-only verifier tests; synthetic transcripts never count as Go evidence."""

import importlib.util
import json
import unittest
from pathlib import Path


SPEC = importlib.util.spec_from_file_location("issue895_red", Path(__file__).with_name("assert-issue895-review-red.py"))
GATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GATE)


def evidence():
    events = [{"Action": "start", "Package": GATE.PACKAGE}]
    for name, result in GATE.expected_results().items():
        if name is not None:
            events.append({"Action": "run", "Test": name})
        if name in GATE.MARKERS:
            events.append({"Action": "output", "Test": name, "Output": GATE.MARKERS[name]})
        events.append({"Action": result, **({"Test": name} if name is not None else {})})
    for event in events:
        event["Package"] = GATE.PACKAGE
    return events


def transcript(events):
    return "".join(json.dumps(event) + "\n" for event in events)


class Issue895ReviewRedTest(unittest.TestCase):
    def test_accepts_exact_expected_failures_and_controls(self):
        GATE.validate(1, transcript(evidence()))
        self.assertEqual(8, len(GATE.MARKERS))
        self.assertEqual(10, len(GATE.CONTROLS))

    def test_rejects_any_extra_control_failure_including_intact(self):
        for name in GATE.CONTROLS:
            with self.subTest(name=name):
                events = evidence()
                for event in events:
                    if event.get("Test") == name and event["Action"] == "pass":
                        event["Action"] = "fail"
                with self.assertRaises(ValueError):
                    GATE.validate(1, transcript(events))

    def test_rejects_wrong_business_assertion(self):
        for name in GATE.MARKERS:
            events = evidence()
            for event in events:
                if event.get("Test") == name and event["Action"] == "output":
                    event["Output"] = "unrelated assertion failure"
            with self.assertRaises(ValueError):
                GATE.validate(1, transcript(events))

    def test_rejects_missing_runs_leaves_or_terminals_and_duplicates(self):
        for name, result in GATE.expected_results().items():
            events = evidence()
            terminal = next(event for event in events if event.get("Test") == name and event["Action"] == result)
            for bad in ([event for event in events if event is not terminal], events + [terminal]):
                with self.assertRaises(ValueError):
                    GATE.validate(1, transcript(bad))
            if name is not None:
                with self.assertRaises(ValueError):
                    GATE.validate(1, transcript([event for event in events if event.get("Test") != name]))
                run = next(event for event in events if event.get("Test") == name and event["Action"] == "run")
                for bad in ([event for event in events if event is not run], events + [run]):
                    with self.assertRaises(ValueError):
                        GATE.validate(1, transcript(bad))

    def test_rejects_unselected_failure_and_incorrect_parent_or_package(self):
        events = evidence()
        with self.assertRaises(ValueError):
            GATE.validate(1, transcript(events + [{"Package": GATE.PACKAGE, "Action": "fail", "Test": "TestUnexpected"}]))
        for name, result in GATE.expected_results().items():
            bad = evidence()
            for event in bad:
                if event.get("Test") == name and event["Action"] == result:
                    event["Action"] = "pass" if result == "fail" else "fail"
            with self.assertRaises(ValueError):
                GATE.validate(1, transcript(bad))

    def test_rejects_compile_setup_skip_panic_timeout_and_abnormal_exit(self):
        for message in ("build failed", "undefined: fixture", "setup failed", "no tests to run",
                        "panic: fixture", "panic_recovered", "fatal error: fixture", "runtime error: fixture",
                        "WARNING: DATA RACE", "signal: killed", "test timed out", "timeout", "context deadline exceeded", "--- SKIP:"):
            for suffix in (message, transcript([{"Package": GATE.PACKAGE, "Action": "output", "Output": message}])):
                with self.subTest(message=message), self.assertRaises(ValueError):
                    GATE.validate(1, transcript(evidence()) + suffix)
        for action in ("skip", "build-fail"):
            with self.assertRaises(ValueError):
                GATE.validate(1, transcript(evidence() + [{"Package": GATE.PACKAGE, "Action": action}]))
        for code in (0, 2, 124, 137, -9):
            with self.assertRaises(ValueError):
                GATE.validate(code, transcript(evidence()))


if __name__ == "__main__":
    unittest.main()
