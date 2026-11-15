import importlib.util
import json
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("evidence", Path(__file__).with_name("ws-owner-conflict-evidence.py"))
EVIDENCE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EVIDENCE)


def fixture(suite):
    leaves = EVIDENCE.SUITES[suite]
    tests = leaves | {leaf.split("/")[0] for leaf in leaves}
    outcome = "fail" if suite == "red" else "pass"
    rows = [{"Package": EVIDENCE.PACKAGE, "Test": name, "Action": action} for name in sorted(tests) for action in ("run", outcome)]
    rows.append({"Package": EVIDENCE.PACKAGE, "Action": outcome})
    if suite == "red":
        rows.append({"Package": EVIDENCE.PACKAGE, "Test": next(iter(leaves)), "Action": "output",
                     "Output": "MIXED_COMPLETED_OWNER_NOT_RESUMED code=encrypted_agent_recovery_owner_conflict"})
    return rows


class EvidenceTests(unittest.TestCase):
    def test_complete_inventories(self):
        for suite in EVIDENCE.SUITES:
            EVIDENCE.validate(suite, 1 if suite == "red" else 0, "\n".join(map(json.dumps, fixture(suite))))

    def test_false_red_rejected(self):
        for mutation in ("compile", "skip", "panic", "race", "unrelated", "wrong_marker", "duplicate", "missing"):
            with self.subTest(mutation=mutation):
                rows = fixture("red")
                if mutation == "compile": rows = []
                if mutation == "skip": rows[1]["Action"] = "skip"
                if mutation in ("panic", "race"): rows[-1]["Output"] += " panic: fixture" if mutation == "panic" else " WARNING: DATA RACE"
                if mutation == "unrelated": rows[-1]["Test"] = "OtherTest"
                if mutation == "wrong_marker": rows[-1]["Output"] = "unexpected EOF"
                if mutation == "duplicate": rows.append(rows[0])
                if mutation == "missing": rows.pop(0)
                with self.assertRaises(ValueError):
                    EVIDENCE.validate("red", 1, "\n".join(map(json.dumps, rows)))

    def test_partial_green_rejected(self):
        rows = fixture("transports")
        rows = [r for r in rows if r.get("Test") != "TestOpenAIWSMixedOwnerTransports/ctx_pool/fresh"]
        with self.assertRaises(ValueError):
            EVIDENCE.validate("transports", 0, "\n".join(map(json.dumps, rows)))


if __name__ == "__main__":
    unittest.main()
