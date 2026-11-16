"""Synthetic parser checks only; these are never Go RED/GREEN evidence."""

import importlib.util
import json
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location(
    "evidence", Path(__file__).with_name("ws-inherited-model-evidence.py")
)
GATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GATE)


def transcript(suite="handler", phase="RED"):
    package, tests, red, positives, fences = GATE.contract(suite)
    package = "github.com/Wei-Shaw/sub2api/internal/" + package
    failures = set()
    if red and phase == "RED":
        for leaf in red:
            parts = leaf.split("/")
            failures.update("/".join(parts[:i]) for i in range(1, len(parts) + 1))
        failures.add(None)
    rows = [{"Package": package, "Action": "start"}]
    for name in sorted(tests):
        rows.append({"Package": package, "Action": "run", "Test": name})
        if suite == "pending" and name in positives:
            output = "PENDING_OBSERVATION_LOST: owner_pending=1" if name in failures else "PENDING_OBSERVATION_CONTROL_OK: classification preserved; no ownership publication"
        elif name in red and name in failures:
            output = GATE.MARKER + " T1/T2 completed on A; T2 omitted model; O1/O2 and C delivered; proof=failed; T3 tool_provenance_unknown; A disabled; B sends=0; records unchanged"
        elif name in positives:
            output = "INHERITED_MODEL_TOOL_LOOP_OK: B sends=1; executions=1; A disabled"
        elif name in fences:
            output = "INHERITED_MODEL_FENCE_OK: B sends=0; records unchanged"
        else:
            output = "synthetic parent event"
        rows.append({"Package": package, "Action": "output", "Test": name, "Output": output})
        rows.append({"Package": package, "Action": "fail" if name in failures else "pass", "Test": name})
    rows.append({"Package": package, "Action": "fail" if failures else "pass"})
    return rows


def encode(rows):
    return "\n".join(json.dumps(row) for row in rows) + "\n"


class EvidenceTests(unittest.TestCase):
    def test_complete_contracts(self):
        for suite in ("handler", "guards", "guards_with_client_model", "pending"):
            for phase in ("RED", "GREEN"):
                with self.subTest(suite=suite, phase=phase):
                    status = int(suite in {"handler", "pending"} and phase == "RED")
                    result = GATE.validate(suite, phase, status, encode(transcript(suite, phase)))
                    self.assertEqual(status * len(GATE.contract(suite)[2]), result["behavioral_red_leaves"])

    def test_pending_requires_exact_failure_and_all_error_controls(self):
        for change in ("marker", "missing_backend_error", "failed_control", "skip", "duplicate", "wrong_status"):
            with self.subTest(change=change):
                rows = transcript("pending")
                status = 1
                if change == "marker":
                    for row in rows:
                        row["Output"] = row.get("Output", "").replace("PENDING_OBSERVATION_LOST:", "other failure")
                elif change == "missing_backend_error":
                    rows = [r for r in rows if r.get("Test") != GATE.PENDING_ROOT + "/pending_backend_deadline"]
                elif change == "failed_control":
                    for row in rows:
                        if row.get("Test") == GATE.PENDING_ROOT + "/pending_backend_deadline" and row["Action"] == "pass":
                            row["Action"] = "fail"
                elif change == "skip":
                    rows[3]["Action"] = "skip"
                elif change == "duplicate":
                    rows.append(rows[-1])
                else:
                    status = 0
                with self.assertRaises(ValueError):
                    GATE.validate("pending", "RED", status, encode(rows))

    def test_runtime_build_and_skip_contamination(self):
        for diagnostic in ("[build failed]", "undefined: fixture", "panic: fixture", "WARNING: DATA RACE", "test timed out after 5m", "no tests to run", "--- SKIP: fixture", "signal: killed"):
            for decoded in (False, True):
                with self.subTest(diagnostic=diagnostic, decoded=decoded):
                    rows = transcript()
                    if decoded:
                        rows[2]["Output"] += "\n" + diagnostic
                        raw = encode(rows)
                    else:
                        raw = encode(rows) + diagnostic
                    with self.assertRaises((ValueError, json.JSONDecodeError)):
                        GATE.validate("handler", "RED", 1, raw)

    def test_missing_repeated_unrelated_and_wrong_outcomes(self):
        for mutation in ("missing_control", "duplicate_run", "duplicate_terminal", "unrelated", "package", "skip", "wrong_failure", "missing_marker", "truncated", "empty"):
            with self.subTest(mutation=mutation):
                rows = transcript()
                if mutation == "missing_control":
                    rows = [r for r in rows if r.get("Test") != GATE.FENCES + "/function/pending"]
                elif mutation == "duplicate_run":
                    rows.append(dict(rows[1]))
                elif mutation == "duplicate_terminal":
                    rows.append(dict(rows[-1]))
                elif mutation == "unrelated":
                    rows.append({"Package": rows[0]["Package"], "Action": "fail", "Test": "TestOther"})
                elif mutation == "package":
                    rows[0]["Package"] += "_other"
                elif mutation == "skip":
                    rows[3]["Action"] = "skip"
                elif mutation == "wrong_failure":
                    for row in rows:
                        if row.get("Action") == "pass" and row.get("Test") == GATE.FENCES + "/function/pending":
                            row["Action"] = "fail"
                elif mutation == "missing_marker":
                    for row in rows:
                        row["Output"] = row.get("Output", "").replace(GATE.MARKER, "unrelated assertion")
                elif mutation == "truncated":
                    rows = rows[:-1]
                elif mutation == "empty":
                    rows = []
                with self.assertRaises(ValueError):
                    GATE.validate("handler", "RED", 1, encode(rows))

    def test_status_phase_and_malformed_input(self):
        for status in (0, 2, 137):
            with self.subTest(status=status), self.assertRaises(ValueError):
                GATE.validate("handler", "RED", status, encode(transcript()))
        with self.assertRaises(ValueError):
            GATE.validate("handler", "GREEN", 0, encode(transcript()))
        for raw in ("{malformed\n", "[]\n", "null\n", "not JSON\n"):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                GATE.validate("handler", "RED", 1, raw)


if __name__ == "__main__":
    unittest.main()
