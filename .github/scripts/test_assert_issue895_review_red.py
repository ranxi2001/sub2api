"""Remote-only verifier tests; synthetic transcripts never count as Go evidence."""

import importlib.util
import json
import unittest
from pathlib import Path


SPEC = importlib.util.spec_from_file_location("issue895_red", Path(__file__).with_name("assert-issue895-review-red.py"))
GATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GATE)


# Independent oracle for the actual Go fixture: four roots and eighteen
# direct t.Run children, with no events for slash prefixes inside their names.
EXPECTED = {
    "TestIssue892OpaqueEnvironmentProjectionRefused": "fail",
    "TestIssue892OpaqueEnvironmentProjectionRefused/apikey/http": "fail",
    "TestIssue892OpaqueEnvironmentProjectionRefused/apikey/ws": "fail",
    "TestIssue892OpaqueEnvironmentProjectionRefused/oauth/http": "fail",
    "TestIssue892OpaqueEnvironmentProjectionRefused/oauth/ws": "fail",
    "TestIssue892ActualSendOpaqueIntegrityAndDelivery": "fail",
    "TestIssue892ActualSendOpaqueIntegrityAndDelivery/http/intact": "pass",
    "TestIssue892ActualSendOpaqueIntegrityAndDelivery/http/envelope": "fail",
    "TestIssue892ActualSendOpaqueIntegrityAndDelivery/http/drop": "fail",
    "TestIssue892ActualSendOpaqueIntegrityAndDelivery/ws/intact": "pass",
    "TestIssue892ActualSendOpaqueIntegrityAndDelivery/ws/envelope": "fail",
    "TestIssue892ActualSendOpaqueIntegrityAndDelivery/ws/drop": "fail",
    "TestIssue892PreparedUnknownOwnerRechecksFences": "pass",
    "TestIssue892PreparedUnknownOwnerRechecksFences/http/pending": "pass",
    "TestIssue892PreparedUnknownOwnerRechecksFences/http/failed": "pass",
    "TestIssue892PreparedUnknownOwnerRechecksFences/ws/pending": "pass",
    "TestIssue892PreparedUnknownOwnerRechecksFences/ws/failed": "pass",
    "TestIssue892ManualTimezone400Recovery": "pass",
    "TestIssue892ManualTimezone400Recovery/enabled": "pass",
    "TestIssue892ManualTimezone400Recovery/disabled": "pass",
    "TestIssue892ManualTimezone400Recovery/ordinary400": "pass",
    "TestIssue892ManualTimezone400Recovery/SSE": "pass",
    None: "fail",
}


def evidence():
    events = [{"Action": "start", "Package": GATE.PACKAGE}]
    for root, result in EXPECTED.items():
        if root is None or "/" in root:
            continue
        events.append({"Action": "run", "Test": root})
        for name, terminal in EXPECTED.items():
            if name is None or not name.startswith(root + "/"):
                continue
            events.append({"Action": "run", "Test": name})
            if terminal == "fail":
                marker = ("Issue895 F1 projection must refuse a changed opaque envelope"
                          if root == "TestIssue892OpaqueEnvironmentProjectionRefused"
                          else "Issue895 F1 actual send must reject a changed opaque item")
                events.append({"Action": "output", "Test": name, "Output": marker})
            events.append({"Action": terminal, "Test": name})
        events.append({"Action": result, "Test": root})
    events.append({"Action": "fail"})
    for event in events:
        event["Package"] = GATE.PACKAGE
    return events


def transcript(events):
    return "".join(json.dumps(event) + "\n" for event in events)


class Issue895ReviewRedTest(unittest.TestCase):
    def test_accepts_exact_expected_failures_and_controls(self):
        GATE.validate(1, transcript(evidence()))
        self.assertEqual(EXPECTED, GATE.expected_results())
        self.assertEqual(8, len(GATE.MARKERS))
        self.assertEqual(10, len(GATE.CONTROLS))

    def test_accepts_no_intermediate_prefix_events_and_rejects_invented_ones(self):
        GATE.validate(1, transcript(evidence()))
        for prefix in ("TestIssue892OpaqueEnvironmentProjectionRefused/apikey",
                       "TestIssue892OpaqueEnvironmentProjectionRefused/oauth",
                       "TestIssue892ActualSendOpaqueIntegrityAndDelivery/http",
                       "TestIssue892ActualSendOpaqueIntegrityAndDelivery/ws",
                       "TestIssue892PreparedUnknownOwnerRechecksFences/http",
                       "TestIssue892PreparedUnknownOwnerRechecksFences/ws"):
            self.assertNotIn(prefix, EXPECTED)
            extra = [{"Package": GATE.PACKAGE, "Action": action, "Test": prefix}
                     for action in ("run", "fail")]
            with self.assertRaises(ValueError):
                GATE.validate(1, transcript(evidence() + extra))

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
        for name, result in EXPECTED.items():
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
        for name, result in EXPECTED.items():
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
