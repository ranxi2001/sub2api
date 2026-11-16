"""Validator self-tests; synthetic events are never business evidence."""

import importlib.util
import json
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("gate", Path(__file__).with_name("assert-account-proxy-config-switch.py"))
GATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GATE)


def evidence(phase):
    terminal = "fail" if phase == "RED" else "pass"
    events = [{"Action": "start"}]
    for root in sorted(GATE.ROOTS):
        events.append({"Action": "run", "Test": root})
        for leaf in sorted(name for name in GATE.LEAVES if name.startswith(root + "/")):
            events.extend([{"Action": "run", "Test": leaf}, {"Action": "output", "Test": leaf, "Output": " ".join(GATE.REACHED) + (" " + GATE.ESCAPED if phase == "RED" else "")}, {"Action": terminal, "Test": leaf}])
        events.append({"Action": terminal, "Test": root})
    events.append({"Action": terminal})
    return [dict(event, Package=GATE.PACKAGE) for event in events]


def raw(events):
    return "\n".join(json.dumps(event) for event in events)


class ConfigSwitchEvidenceTests(unittest.TestCase):
    def test_complete_red_and_green(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            self.assertEqual(8, GATE.validate(phase, code, raw(evidence(phase)))["leaves"])

    def test_each_run_terminal_and_business_marker_required(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            events = evidence(phase)
            for index, event in enumerate(events):
                if event["Action"] in {"run", "pass", "fail", "output"}:
                    with self.assertRaises(ValueError):
                        GATE.validate(phase, code, raw(events[:index] + events[index + 1:]))
                if event["Action"] in {"run", "pass", "fail"}:
                    with self.assertRaises(ValueError):
                        GATE.validate(phase, code, raw(events + [event]))
                if event["Action"] == "output":
                    for marker in (*GATE.REACHED, GATE.ESCAPED) if phase == "RED" else GATE.REACHED:
                        changed = [dict(value) for value in events]
                        changed[index]["Output"] = changed[index]["Output"].replace(marker, "unrelated")
                        with self.assertRaises(ValueError):
                            GATE.validate(phase, code, raw(changed))

    def test_exit_verdict_scope_and_contamination_rejected(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            events = evidence(phase)
            for wrong in (0, 1, 2, 137):
                if wrong != code:
                    with self.assertRaises(ValueError):
                        GATE.validate(phase, wrong, raw(events))
            for action in ("pass", "fail", "run", "skip", "build-fail"):
                with self.assertRaises(ValueError):
                    GATE.validate(phase, code, raw(events + [{"Action": action, "Test": "Unrelated", "Package": GATE.PACKAGE}]))
            for poison in ("[no tests to run]", "DATA RACE", "panic: bad", "undefined: fixture", "no required module provides package", "--- SKIP: fixture"):
                with self.assertRaises(ValueError):
                    GATE.validate(phase, code, raw(events + [{"Action": "output", "Package": GATE.PACKAGE, "Output": poison}]))
            for bad in ("", "not JSON", "[]", raw([dict(events[0], Package="other")]) + "\n" + raw(events)):
                with self.assertRaises(ValueError):
                    GATE.validate(phase, code, bad)

    def test_mixed_verdict_and_wrong_phase_rejected(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            events = evidence(phase)
            for index, event in enumerate(events):
                if event["Action"] not in {"pass", "fail"}:
                    continue
                changed = [dict(value) for value in events]
                changed[index]["Action"] = "pass" if phase == "RED" else "fail"
                with self.assertRaises(ValueError):
                    GATE.validate(phase, code, raw(changed))
        with self.assertRaises(ValueError):
            GATE.validate("OTHER", 1, raw(evidence("RED")))


if __name__ == "__main__":
    unittest.main()
