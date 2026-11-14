"""Remote-only evidence-validator tests; no fixture or provider is executed here."""

import importlib.util
import json
import unittest
from pathlib import Path


SPEC = importlib.util.spec_from_file_location("shadow_gate", Path(__file__).with_name("assert-future-shadow.py"))
GATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GATE)


def evidence(phase, boundaries=False, resources=False, association=False):
    root, leaves = (GATE.BOUNDARIES, GATE.BOUNDARY_LEAVES) if boundaries else (GATE.ROOT, GATE.LEAVES)
    if resources:
        root, leaves = GATE.RESOURCES, GATE.RESOURCE_LEAVES
    if association:
        root, leaves = GATE.ASSOCIATION, GATE.ASSOCIATION_LEAVES
    terminal = "fail" if phase == "RED" else "pass"
    events = [{"Action": "run", "Test": root}]
    for name in leaves:
        events.append({"Action": "run", "Test": name})
        if phase == "RED":
            output = "SHADOW_TASK_ASSOCIATION_MISMATCH" if association else "FUTURE_SHADOW_B_DISPATCH_MISSING: encrypted_agent_recovery_readable_intent_missing"
            events.append({"Action": "output", "Test": name, "Output": output})
        events.append({"Action": terminal, "Test": name})
    events.extend([{"Action": terminal, "Test": root}, {"Action": terminal}])
    for event in events:
        event["Package"] = GATE.COMMON.PACKAGE
    return events


def transcript(events):
    return "".join(json.dumps(event) + "\n" for event in events)


class FutureShadowEvidenceTest(unittest.TestCase):
    def test_valid_evidence(self):
        GATE.validate("RED", 1, transcript(evidence("RED")))
        GATE.validate("GREEN", 0, transcript(evidence("GREEN")))
        GATE.validate("GREEN", 0, transcript(evidence("GREEN", True)), True)
        GATE.validate("GREEN", 0, transcript(evidence("GREEN", resources=True)), resources=True)
        for phase, code in (("RED", 1), ("GREEN", 0)):
            GATE.validate(phase, code, transcript(evidence(phase, association=True)), association=True)

    def test_association_mutant_needs_content_failure_for_each_task(self):
        for name in GATE.ASSOCIATION_LEAVES:
            events = evidence("RED", association=True)
            for event in events:
                if event.get("Test") == name and event["Action"] == "output":
                    event["Output"] = "FUTURE_SHADOW_B_DISPATCH_MISSING: encrypted_agent_recovery_readable_intent_missing"
            with self.assertRaises(ValueError):
                GATE.validate("RED", 1, transcript(events), association=True)

    def test_resources_require_every_named_leaf(self):
        for name in GATE.RESOURCE_LEAVES:
            events = [event for event in evidence("GREEN", resources=True) if event.get("Test") != name]
            with self.assertRaises(ValueError):
                GATE.validate("GREEN", 0, transcript(events), resources=True)

    def test_each_business_failure_needs_its_own_diagnostic(self):
        for name in GATE.LEAVES:
            for output in ("FUTURE_SHADOW_B_DISPATCH_MISSING", "encrypted_agent_recovery_readable_intent_missing", "unrelated error"):
                with self.subTest(name=name, output=output):
                    events = evidence("RED")
                    for event in events:
                        if event.get("Test") == name and event["Action"] == "output":
                            event["Output"] = output
                    with self.assertRaises(ValueError):
                        GATE.validate("RED", 1, transcript(events))

    def test_pollution_in_raw_and_json_output(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            for diagnostic in ("panic: fixture", "openai.usage_record_task_panic_recovered", "WARNING: DATA RACE", "fatal error: crash", "signal: killed", "--- SKIP: fixture", "undefined: fixture", "[no tests to run]"):
                for encoded in (False, True):
                    with self.subTest(phase=phase, diagnostic=diagnostic, encoded=encoded):
                        poison = transcript([{"Action": "output", "Output": diagnostic}]) if encoded else diagnostic
                        with self.assertRaises(ValueError):
                            GATE.validate(phase, code, transcript(evidence(phase)) + poison)

    def test_missing_run_terminal_and_skip(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            events = evidence(phase)
            for bad in (events[1:], events[:-1], events + [{"Action": "skip"}]):
                with self.assertRaises(ValueError):
                    GATE.validate(phase, code, transcript(bad))


if __name__ == "__main__":
    unittest.main()
