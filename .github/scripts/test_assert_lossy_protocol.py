import contextlib
import importlib.util
import io
import json
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("gate", Path(__file__).with_name("assert-lossy-protocol.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


def transcript(phase):
    package = "github.com/Wei-Shaw/sub2api/internal/handler"
    rows = []
    for root in (gate.HTTP, gate.WS):
        for lossy in ("false", "true"):
            for account in ("apikey", "oauth"):
                for stream in (("false", "true") if root == gate.HTTP else (None,)):
                    name = f"{root}/lossy={lossy}/{account}" + (f"/stream={stream}" if stream else "")
                    fail = phase == "RED" and (root == gate.WS or lossy == "true")
                    rows += [{"Package": package, "Test": name, "Action": "run"},
                             {"Package": package, "Test": name, "Action": "output", "Output": "LOSSY_TOOL_PROTOCOL_LOST" if fail else "completed"},
                             {"Package": package, "Test": name, "Action": "fail" if fail else "pass"}]
    rows.append({"Package": package, "Action": "fail" if phase == "RED" else "pass"})
    return rows


class EvidenceGateTest(unittest.TestCase):
    def check(self, phase, rows, status=None, suffix=""):
        with contextlib.redirect_stdout(io.StringIO()):
            gate.validate(phase, (1 if phase == "RED" else 0) if status is None else status,
                          "\n".join(map(json.dumps, rows)) + suffix, "loops")

    def test_complete_red_and_green(self):
        for phase in ("RED", "GREEN"):
            self.check(phase, transcript(phase))

    def test_false_control_must_pass(self):
        rows = transcript("RED")
        rows[2]["Action"] = "fail"
        with self.assertRaises(ValueError):
            self.check("RED", rows)

    def test_missing_or_duplicate_leaf(self):
        rows = transcript("GREEN")
        for altered in (rows[3:], rows + rows[:3]):
            with self.assertRaises(ValueError):
                self.check("GREEN", altered)

    def test_runtime_compile_skip_and_empty_are_not_red(self):
        for pollution in ("undefined: UsageStartTime", "panic: failure", "WARNING: DATA RACE", "no tests to run"):
            with self.assertRaises(ValueError):
                self.check("RED", transcript("RED"), suffix="\n" + pollution)
        with self.assertRaises(ValueError):
            self.check("RED", [])
        rows = transcript("RED")
        rows.append({"Action": "skip"})
        with self.assertRaises(ValueError):
            self.check("RED", rows)

    def test_unrelated_failure_or_wrong_exit(self):
        rows = transcript("RED")
        rows.append({"Package": rows[0]["Package"], "Test": "TestUnrelated", "Action": "fail"})
        with self.assertRaises(ValueError):
            self.check("RED", rows)
        with self.assertRaises(ValueError):
            self.check("RED", transcript("RED"), status=2)

    def test_discovery_blocker_requires_each_named_business_red(self):
        package = "github.com/Wei-Shaw/sub2api/internal/service"
        rows = []
        for case in sorted(gate.DISCOVERY_RED):
            name = f"{gate.DISCOVERY}/{case}"
            rows.extend([{"Package": package, "Test": name, "Action": "run"},
                         {"Package": package, "Test": name, "Action": "output", "Output": "DISCOVERY_UNTRUSTED_HISTORY_ACCEPTED"},
                         {"Package": package, "Test": name, "Action": "fail"}])
        rows.append({"Package": package, "Action": "fail"})
        with contextlib.redirect_stdout(io.StringIO()):
            gate.validate("RED", 1, "\n".join(map(json.dumps, rows)), "discovery")
        rows[1]["Output"] = "unrelated assertion"
        with self.assertRaises(ValueError), contextlib.redirect_stdout(io.StringIO()):
            gate.validate("RED", 1, "\n".join(map(json.dumps, rows)), "discovery")


if __name__ == "__main__":
    unittest.main()
