"""Remote-only self-tests for the HTTP task-rebuild evidence manifest."""

import importlib.util
import json
import unittest
from pathlib import Path


SPEC = importlib.util.spec_from_file_location(
    "task_rebuild_evidence", Path(__file__).with_name("assert-http-task-rebuild.py")
)
GATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GATE)


def evidence(phase, boundaries=False):
    root, leaves = (GATE.BOUNDARIES, GATE.BOUNDARY_LEAVES) if boundaries else (GATE.ROOT, GATE.LEAVES)
    terminal = "fail" if phase == "RED" else "pass"
    events = [{"Action": "run", "Test": root, "Package": GATE.PACKAGE}]
    for name in sorted(leaves):
        events.append({"Action": "run", "Test": name, "Package": GATE.PACKAGE})
        if phase == "RED":
            events.append({"Action": "output", "Test": name, "Package": GATE.PACKAGE,
                           "Output": GATE.DIAGNOSTIC + ": response=" + GATE.OLD_REFUSALS[0] + "\n"})
        events.append({"Action": terminal, "Test": name, "Package": GATE.PACKAGE})
    events.extend([{"Action": terminal, "Test": root, "Package": GATE.PACKAGE},
                   {"Action": terminal, "Package": GATE.PACKAGE}])
    return events


def transcript(events):
    return "".join(json.dumps(event) + "\n" for event in events)


class HTTPTaskRebuildEvidenceTest(unittest.TestCase):
    def test_valid_red_green_and_boundaries(self):
        GATE.validate("RED", 1, transcript(evidence("RED")))
        GATE.validate("GREEN", 0, transcript(evidence("GREEN")))
        GATE.validate("GREEN", 0, transcript(evidence("GREEN", True)), True)

    def test_each_named_red_requires_assertion_and_refusal(self):
        for name in GATE.LEAVES:
            for replacement in (GATE.DIAGNOSTIC, GATE.OLD_REFUSALS[0], "unrelated assertion"):
                with self.subTest(name=name, replacement=replacement):
                    events = evidence("RED")
                    for event in events:
                        if event.get("Test") == name and event["Action"] == "output":
                            event["Output"] = replacement
                    with self.assertRaisesRegex(ValueError, "missing named B-dispatch"):
                        GATE.validate("RED", 1, transcript(events))

    def test_contamination_is_never_business_red_or_green(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            for diagnostic in ("panic: failed", "openai.usage_record_task_panic_recovered", "WARNING: DATA RACE", "fatal error: crash", "signal: killed", "--- SKIP: fixture", "undefined: fixture", "[no tests to run]"):
                for raw in (False, True):
                    with self.subTest(phase=phase, diagnostic=diagnostic, raw=raw):
                        poison = diagnostic if raw else transcript([{"Action": "output", "Package": GATE.PACKAGE, "Output": diagnostic}])
                        with self.assertRaises(ValueError):
                            GATE.validate(phase, code, transcript(evidence(phase)) + poison)

    def test_missing_run_package_terminal_and_skip(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            events = evidence(phase)
            for altered in (events[1:], events[:-1], events + [{"Action": "skip", "Package": GATE.PACKAGE}]):
                with self.subTest(phase=phase):
                    with self.assertRaises(ValueError):
                        GATE.validate(phase, code, transcript(altered))

    def test_mutant_requires_actual_extra_generation_assertion(self):
        for mutant, name in GATE.MUTANTS.items():
            values = [
                {"Action": "run", "Test": GATE.MIXED, "Package": GATE.PACKAGE},
                {"Action": "run", "Test": name, "Package": GATE.PACKAGE},
                {"Action": "output", "Test": name, "Package": GATE.PACKAGE,
                 "Output": "HTTP_REBUILD_FENCE_BYPASSED: response=coding-task-complete"},
                {"Action": "fail", "Test": name, "Package": GATE.PACKAGE},
                {"Action": "fail", "Test": GATE.MIXED, "Package": GATE.PACKAGE},
                {"Action": "fail", "Package": GATE.PACKAGE},
            ]
            GATE.validate("MUTANT", 1, transcript(values), mutant=mutant)
            for bad in ("unrelated assertion", "HTTP_REBUILD_FENCE_BYPASSED", "panic: coding-task-complete"):
                with self.subTest(mutant=mutant, bad=bad):
                    values[2]["Output"] = bad
                    with self.assertRaises(ValueError):
                        GATE.validate("MUTANT", 1, transcript(values), mutant=mutant)

    def test_all_new_green_inventory_is_required(self):
        for suite, (root, leaves) in GATE.SUITES.items():
            values = [{"Action": "run", "Test": root, "Package": GATE.PACKAGE}]
            for name in leaves:
                values.extend([{"Action": "run", "Test": name, "Package": GATE.PACKAGE},
                               {"Action": "pass", "Test": name, "Package": GATE.PACKAGE}])
            values.extend([{"Action": "pass", "Test": root, "Package": GATE.PACKAGE},
                           {"Action": "pass", "Package": GATE.PACKAGE}])
            GATE.validate("GREEN", 0, transcript(values), suite=suite)
            with self.assertRaises(ValueError):
                GATE.validate("GREEN", 0, transcript(values[1:]), suite=suite)

    def test_review_red_requires_specific_failure(self):
        for suite in ("normalization", "pointer-waiver"):
            root, leaves = GATE.SUITES[suite]
            events = [{"Action": "run", "Test": root, "Package": GATE.PACKAGE}]
            for name in leaves:
                if suite == "pointer-waiver":
                    output = "UNOWNED_POINTER_WAIVER_LEAK: response=completed"
                elif name.endswith("/blank_parts"):
                    output = "HTTP_REBUILD_B_DISPATCH_MISSING encrypted_agent_recovery_readable_intent_missing"
                elif name.endswith("/no_tools_parallel"):
                    output = "automatic next tool turn must finish encrypted_agent_recovery_wire_changed"
                else:
                    output = "HTTP_REBUILD_B_DISPATCH_MISSING encrypted_agent_recovery_wire_changed"
                events.extend([{"Action": "run", "Test": name, "Package": GATE.PACKAGE},
                               {"Action": "output", "Test": name, "Package": GATE.PACKAGE, "Output": output},
                               {"Action": "fail", "Test": name, "Package": GATE.PACKAGE}])
            events.extend([{"Action": "fail", "Test": root, "Package": GATE.PACKAGE},
                           {"Action": "fail", "Package": GATE.PACKAGE}])
            GATE.validate("RED", 1, transcript(events), suite=suite)
            events[2]["Output"] = "arbitrary failure"
            with self.assertRaises(ValueError):
                GATE.validate("RED", 1, transcript(events), suite=suite)


if __name__ == "__main__":
    unittest.main()
