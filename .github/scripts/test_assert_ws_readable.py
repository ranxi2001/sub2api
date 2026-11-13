"""Run only on Actions; validate clean per-leaf WS RED and exact GREEN."""

import importlib.util
import json
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("gate", Path(__file__).with_name("assert-ws-readable.py"))
GATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GATE)


def evidence(phase, suite):
    root, names = GATE.SUITES[suite]
    terminal = "fail" if phase == "RED" else "pass"
    events = [{"Action": "run", "Test": root}]
    for suffix in names:
        name = f"{root}/{suffix}"
        events.append({"Action": "run", "Test": name})
        if phase == "RED":
            events.append({"Action": "output", "Test": name, "Output": " ".join(GATE.diagnostic(suite, name))})
        events.append({"Action": terminal, "Test": name})
    events.extend([{"Action": terminal, "Test": root}, {"Action": terminal}])
    for event in events:
        event["Package"] = GATE.COMMON.PACKAGE.replace("/handler", "/securityaudit") if suite in {"audit-persistence", "audit-async"} else GATE.COMMON.PACKAGE
    return events


def raw(events):
    return "".join(json.dumps(event) + "\n" for event in events)


class WSReadableEvidenceTest(unittest.TestCase):
    def test_valid(self):
        GATE.validate("RED", 1, raw(evidence("RED", "ws")), "ws")
        for suite in GATE.SUITES:
            GATE.validate("GREEN", 0, raw(evidence("GREEN", suite)), suite)
        for suite in GATE.RED_SUITES:
            GATE.validate("RED", 1, raw(evidence("RED", suite)), suite)

    def test_repair_red_needs_each_own_business_diagnostic(self):
        for suite in GATE.RED_SUITES:
            for index, original in enumerate(evidence("RED", suite)):
                if original.get("Action") != "output":
                    continue
                events = evidence("RED", suite)
                events[index]["Output"] = "unrelated error"
                with self.assertRaises(ValueError):
                    GATE.validate("RED", 1, raw(events), suite)

    def test_each_leaf_requires_both_diagnostics(self):
        for suffix in GATE.SUITES["ws"][1]:
            for diagnostic in ("WS_READABLE_B_DISPATCH_MISSING", "account eligibility changed", "other"):
                events = evidence("RED", "ws")
                for event in events:
                    if event.get("Test", "").endswith("/" + suffix) and event["Action"] == "output":
                        event["Output"] = diagnostic
                with self.assertRaises(ValueError):
                    GATE.validate("RED", 1, raw(events), "ws")

    def test_each_green_leaf_required(self):
        for suite, (root, names) in GATE.SUITES.items():
            for suffix in names:
                events = [e for e in evidence("GREEN", suite) if e.get("Test") != f"{root}/{suffix}"]
                with self.assertRaises(ValueError):
                    GATE.validate("GREEN", 0, raw(events), suite)

    def test_contamination(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            for poison in ("panic: fixture", "WARNING: DATA RACE", "openai.usage_record_task_panic_recovered", "fatal error: crash", "signal: killed", "undefined: fixture", "[no tests to run]", "--- SKIP: fixture"):
                for encoded in (False, True):
                    contamination = raw([{"Action": "output", "Output": poison}]) if encoded else poison
                    with self.assertRaises(ValueError):
                        GATE.validate(phase, code, raw(evidence(phase, "ws")) + contamination, "ws")


if __name__ == "__main__":
    unittest.main()
