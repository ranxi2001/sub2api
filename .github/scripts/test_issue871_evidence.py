import importlib.util
import json
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("gate", Path(__file__).with_name("issue871-evidence.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


def transcript(phase, contract="diagnosis"):
    root, leaf, marker, assertion = gate.ROOT, gate.LEAF, gate.MARKER, gate.ASSERTION
    if contract == "business":
        root, leaf = gate.BUSINESS_ROOT, gate.BUSINESS_LEAF
        marker, assertion = "", gate.BUSINESS_ASSERTION
    elif contract == "pending_deadline":
        root, leaf = gate.PENDING_ROOT, gate.PENDING_LEAF
        marker, assertion = "", gate.PENDING_ASSERTION
    terminal = "fail" if phase == "RED" else "pass"
    events = [dict(Action="start", Package=gate.PACKAGE)]
    for name in (root, leaf):
        events.append(dict(Action="run", Package=gate.PACKAGE, Test=name))
    if phase == "RED":
        events.append(dict(Action="output", Package=gate.PACKAGE, Test=leaf,
                           Output=marker + "\n" + assertion + "\n"))
    for name in (leaf, root):
        events.append(dict(Action=terminal, Package=gate.PACKAGE, Test=name))
    events.append(dict(Action=terminal, Package=gate.PACKAGE))
    return "\n".join(json.dumps(event) for event in events)


class Issue871EvidenceTest(unittest.TestCase):
    def test_pending_deadline_contract(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            raw = transcript(phase, "pending_deadline")
            gate.validate(phase, code, raw, contract="pending_deadline")
            for noise in ("panic: fixture", "build failed", "setup failed", "no tests to run",
                          "DATA RACE", "--- SKIP: fixture", "test timed out", "fatal error"):
                with self.subTest(phase=phase, noise=noise), self.assertRaises(ValueError):
                    gate.validate(phase, code, raw, noise, "pending_deadline")
            for invalid in ("", raw + "\n" + raw, raw.replace(gate.PENDING_LEAF, gate.PENDING_ROOT + "/wrong"),
                            raw.replace('"Action": "run"', '"Action": "skip"', 1), transcript(phase)):
                with self.subTest(phase=phase, invalid=invalid), self.assertRaises(ValueError):
                    gate.validate(phase, code, invalid, contract="pending_deadline")
            with self.assertRaises(ValueError):
                gate.validate(phase, 2, raw, contract="pending_deadline")
        red = transcript("RED", "pending_deadline")
        for invalid in (red.replace(gate.PENDING_MARKER, "other_assertion"),
                        red.replace("encrypted_agent_recovery_store_unavailable", "other_error")):
            with self.assertRaises(ValueError):
                gate.validate("RED", 1, invalid, contract="pending_deadline")

    def test_business_named_red_and_green(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            raw = transcript(phase, "business")
            gate.validate(phase, code, raw, contract="business")
            with self.assertRaises(ValueError):
                gate.validate(phase, code, raw)
            with self.assertRaises(ValueError):
                gate.validate(phase, code, transcript(phase), contract="business")
            with self.assertRaises(ValueError):
                gate.validate(phase, 2, raw, contract="business")
            for noise in ("panic: fixture", "build failed", "setup failed", "no tests to run",
                          "DATA RACE", "--- SKIP: fixture", "test timed out", "fatal error"):
                with self.subTest(phase=phase, noise=noise), self.assertRaises(ValueError):
                    gate.validate(phase, code, raw, noise, "business")

    def test_business_rejects_wrong_assertion_or_test(self):
        red = transcript("RED", "business")
        for invalid in (
            "", red.replace(gate.BUSINESS_MARKER, "unrelated_assertion"),
            red.replace('__sub2api_web_search', 'different_tool'),
            red.replace('"Action": "run"', '"Action": "skip"', 1),
            red.replace(gate.BUSINESS_LEAF, gate.BUSINESS_ROOT + "/wrapped/json"),
            red + "\n" + red, red + "\nnot JSON",
            red.replace(gate.PACKAGE, "other/package"),
        ):
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                gate.validate("RED", 1, invalid, contract="business")
        with self.assertRaises(ValueError):
            gate.validate("RED", 1, red, contract="typo")

    def test_named_red_and_green(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            gate.validate(phase, code, transcript(phase))

    def test_rejects_nonbehavioral_failure_and_missing_test(self):
        red = transcript("RED")
        for invalid in (
            "", red.replace(gate.MARKER, "unrelated_assertion"),
            red.replace("phase=search_http; reason=http_rejected", "different assertion"),
            red.replace('"Action": "run"', '"Action": "skip"', 1),
            red.replace(gate.LEAF, gate.ROOT + "/wrong_case"), red + "\n" + red,
            red + "\nnot JSON", red.replace(gate.PACKAGE, "other/package"),
        ):
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                gate.validate("RED", 1, invalid)

    def test_rejects_exit_mismatch_and_runtime_pollution(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            raw = transcript(phase)
            with self.assertRaises(ValueError):
                gate.validate(phase, 2, raw)
            for noise in ("panic: fixture", "build failed", "setup failed", "no tests to run",
                          "DATA RACE", "--- SKIP: fixture", "test timed out", "fatal error"):
                with self.subTest(phase=phase, noise=noise), self.assertRaises(ValueError):
                    gate.validate(phase, code, raw, noise)


if __name__ == "__main__":
    unittest.main()
