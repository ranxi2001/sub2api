import importlib.util
import json
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("gate", Path(__file__).with_name("issue854-evidence.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


def transcript(phase, suite="lifecycle"):
    leaves, parents, failures, markers = gate.inventory(suite)
    events = []
    for name in sorted(leaves | parents):
        events.append(dict(Action="run", Test=name, Package=gate.common.PACKAGE))
        failed = phase == "RED" and (name in failures or (name in parents and any(n.startswith(name + "/") for n in failures)))
        if failed:
            events.append(dict(Action="output", Test=name, Package=gate.common.PACKAGE, Output=" ".join(markers)))
        events.append(dict(Action="fail" if failed else "pass", Test=name, Package=gate.common.PACKAGE))
    events.append(dict(Action="fail" if phase == "RED" else "pass", Package=gate.common.PACKAGE))
    return "\n".join(json.dumps(e) for e in events)


class EvidenceTest(unittest.TestCase):
    def test_other_suites(self):
        for suite in ("persistence", "upstream", "ws-upstream", "r1", "http-empty-image"):
            for phase, code in (("RED", 1), ("GREEN", 0)):
                raw = transcript(phase, suite)
                gate.validate(phase, code, raw, suite)
                for pollution in ("build failed", "setup failed", "requires real redis-server in CI", "no tests to run", "panic", "DATA RACE", "--- SKIP: fixture", "crash"):
                    with self.assertRaises(ValueError):
                        gate.validate(phase, code, raw + "\n" + pollution, suite)
                with self.assertRaises(ValueError):
                    gate.validate(phase, code, raw.replace('"Action": "run"', '"Action": "skip"', 1), suite)

    def test_updated_policy_matrix_green(self):
        for suite in ("http-transform", "http-route"):
            raw = transcript("GREEN", suite)
            gate.validate("GREEN", 0, raw, suite)
            for polluted in (raw + "\npanic: fixture", raw + "\n" + raw, ""):
                with self.assertRaises(ValueError):
                    gate.validate("GREEN", 0, polluted, suite)
            with self.assertRaises(ValueError):
                gate.validate("RED", 1, raw, suite)

    def test_valid_and_polluted(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            raw = transcript(phase)
            gate.validate(phase, code, raw)
            for pollution in ("panic: fixture", "DATA RACE", "build failed", "setup failed", "requires real redis-server in CI", "no tests to run", "--- SKIP: x", "signal: killed", "fatal error", "test timed out", "crash"):
                with self.subTest(phase=phase, pollution=pollution), self.assertRaises(ValueError):
                    gate.validate(phase, code, raw + "\n" + pollution)
            with self.assertRaises(ValueError):
                gate.validate(phase, code, raw.replace('"Action": "run"', '"Action": "skip"', 1))
            with self.assertRaises(ValueError):
                gate.validate(phase, code, raw + "\n" + raw)
            with self.assertRaises(ValueError):
                gate.validate(phase, code, "")
        with self.assertRaises(ValueError):
            gate.validate("RED", 1, transcript("RED").replace("WS_READABLE_B_DISPATCH_MISSING", "unrelated error"))


if __name__ == "__main__":
    unittest.main()
