import importlib.util
import json
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("gate", Path(__file__).with_name("issue854-evidence.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


def transcript(phase):
    events = []
    for name in sorted(gate.LEAVES | gate.PARENTS):
        events.append(dict(Action="run", Test=name, Package=gate.common.PACKAGE))
        failed = phase == "RED" and (name in gate.PARENTS or name.endswith("/guard_live"))
        if failed:
            events.append(dict(Action="output", Test=name, Package=gate.common.PACKAGE, Output="WS_READABLE_B_DISPATCH_MISSING: The proven encrypted agent owner is currently unavailable"))
        events.append(dict(Action="fail" if failed else "pass", Test=name, Package=gate.common.PACKAGE))
    events.append(dict(Action="fail" if phase == "RED" else "pass", Package=gate.common.PACKAGE))
    return "\n".join(json.dumps(e) for e in events)


class EvidenceTest(unittest.TestCase):
    def test_valid_and_polluted(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            raw = transcript(phase)
            gate.validate(phase, code, raw)
            for pollution in ("panic: fixture", "DATA RACE", "build failed", "setup failed", "no tests to run", "--- SKIP: x", "signal: killed", "fatal error", "test timed out", "crash"):
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
