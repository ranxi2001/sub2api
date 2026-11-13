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
        if suite == "ws-ticket":
            event["Package"] = GATE.COMMON.PACKAGE.replace("/handler", "/service")
    return events


def raw(events):
    return "".join(json.dumps(event) + "\n" for event in events)


class WSReadableEvidenceTest(unittest.TestCase):
    def strict_baseline(self, failures):
        events = evidence("GREEN", "strict-pointer")
        root, _ = GATE.SUITES["strict-pointer"]
        names = {f"{root}/{suffix}" for suffix in failures}
        for event in events:
            if event["Action"] == "pass" and (event.get("Test") in names or (failures and event.get("Test") in {root, None})):
                event["Action"] = "fail"
        for name in names:
            events.append({"Package": GATE.COMMON.PACKAGE, "Action": "output", "Test": name, "Output": "HTTP_POINTER_STRICT_A_STATE_LEAK"})
        return events

    def test_strict_baseline_pins_each_expected_leaf(self):
        names = GATE.SUITES["strict-pointer"][1]
        failures = {"apikey/stream=false/strict", "apikey/stream=true/strict"}
        events = self.strict_baseline(failures)
        GATE.validate("BASELINE", 1, raw(events), "strict-pointer")
        GATE.validate("GREEN", 0, raw(evidence("GREEN", "strict-pointer")), "strict-pointer")
        for incorrect in (set(), names, names - failures, {"apikey/stream=false/strict", "oauth/stream=true/strict"}):
            with self.assertRaises(ValueError):
                GATE.validate("BASELINE", int(bool(incorrect)), raw(self.strict_baseline(incorrect)), "strict-pointer")
        for code in (0, 1):
            with self.assertRaises(ValueError):
                GATE.validate("GREEN", code, raw(events), "strict-pointer")
        with self.assertRaises(ValueError):
            GATE.validate("RED", 1, raw(self.strict_baseline(names)), "strict-pointer")
        for index, original in enumerate(events):
            if original["Action"] in {"run", "pass", "fail", "output"}:
                with self.assertRaises(ValueError):
                    GATE.validate("BASELINE", 1, raw(events[:index] + events[index + 1:]), "strict-pointer")
            if original["Action"] in {"run", "pass", "fail"}:
                with self.assertRaises(ValueError):
                    GATE.validate("BASELINE", 1, raw(events + [original]), "strict-pointer")
            if original["Action"] in {"pass", "fail"}:
                wrong = dict(original, Action="pass" if original["Action"] == "fail" else "fail")
                with self.assertRaises(ValueError):
                    GATE.validate("BASELINE", 1, raw(events[:index] + [wrong] + events[index + 1:]), "strict-pointer")
        for wrong_exit in (0, 2, 137):
            with self.assertRaises(ValueError):
                GATE.validate("BASELINE", wrong_exit, raw(events), "strict-pointer")
        for poison in ("panic: fixture", "WARNING: DATA RACE", "openai.usage_record_task_panic_recovered", "fatal error: crash", "signal: killed", "undefined: fixture", "[no tests to run]", "--- SKIP: fixture"):
            for contamination in (poison, raw([{"Action": "output", "Output": poison}])):
                with self.assertRaises(ValueError):
                    GATE.validate("BASELINE", 1, raw(events) + contamination, "strict-pointer")
        for action in ("run", "pass", "fail", "skip"):
            with self.assertRaises(ValueError):
                GATE.validate("BASELINE", 1, raw(events + [{"Package": GATE.COMMON.PACKAGE, "Action": action, "Test": "Unrelated"}]), "strict-pointer")
        with self.assertRaises(ValueError):
            GATE.validate("BASELINE", 1, raw(events + [{"Package": "unrelated/package", "Action": "pass"}]), "strict-pointer")

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

    def test_tool_red_requires_actual_transport_message_on_both_leaves(self):
        # Keep this independent of diagnostic(): the actual baseline emits
        # ClientMessage(), not its internal wire_changed reason identifier.
        message = "The proven encrypted agent transport changed."
        marker = "WS_RESIDUAL_TOOL_PROJECTION_REFUSED"
        events = evidence("RED", "ws-residual")
        for event in events:
            if event.get("Action") == "output" and "/tools_" in event.get("Test", ""):
                event["Output"] = f"{marker}: {message} Preserve this task and context."
        GATE.validate("RED", 1, raw(events), "ws-residual")
        for leaf in ("tools_omitted", "tools_explicit"):
            for wrong in (marker, message, f"{marker}: wire_changed", f"{marker}: upstream failed"):
                changed = [dict(event) for event in events]
                for event in changed:
                    if event.get("Action") == "output" and event.get("Test", "").endswith("/" + leaf):
                        event["Output"] = wrong
                with self.assertRaises(ValueError):
                    GATE.validate("RED", 1, raw(changed), "ws-residual")
        for poison in ("undefined: fixture", "panic: fixture", "WARNING: DATA RACE", "--- SKIP: fixture"):
            with self.assertRaises(ValueError):
                GATE.validate("RED", 1, raw(events) + poison, "ws-residual")

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
