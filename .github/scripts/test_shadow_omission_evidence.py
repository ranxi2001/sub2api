import importlib.util
import json
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("evidence", Path(__file__).with_name("shadow-omission-evidence.py"))
EVIDENCE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EVIDENCE)


def fixture(phase):
    rows = []
    for name in sorted(EVIDENCE.LEAVES | {EVIDENCE.AUDIT, EVIDENCE.NEXT}):
        rows.append({"Package": EVIDENCE.PACKAGE, "Action": "run", "Test": name})
        rows.append({"Package": EVIDENCE.PACKAGE, "Action": "fail" if phase == "RED" else "pass", "Test": name})
        if name in EVIDENCE.LEAVES:
            expected = "response.completed" if name.startswith(EVIDENCE.NEXT + "/") else "prompt_guard_blocked" if name.endswith("/prompt") else "content_policy_violation"
            actual = "error" if name.startswith(EVIDENCE.NEXT + "/") else "encrypted_agent_recovery_readable_intent_missing"
            rows.append({"Package": EVIDENCE.PACKAGE, "Action": "output", "Test": name, "Output": f'expected: "{expected}"\nactual  : "{actual}"\nMessages: encrypted_agent_recovery_readable_intent_missing'})
    rows.append({"Package": EVIDENCE.PACKAGE, "Action": "fail" if phase == "RED" else "pass"})
    return rows


class ShadowEvidenceTests(unittest.TestCase):
    def test_complete(self):
        for phase in ("RED", "GREEN"):
            EVIDENCE.validate(phase, 1 if phase == "RED" else 0, "\n".join(map(json.dumps, fixture(phase))))

    def test_false_red_rejected(self):
        for mutation in ("missing", "duplicate", "wrong_refusal", "panic", "skip", "build", "unrelated"):
            with self.subTest(mutation=mutation):
                rows = fixture("RED")
                if mutation == "missing": rows.pop(0)
                if mutation == "duplicate": rows.append(rows[0])
                if mutation == "wrong_refusal":
                    for row in rows:
                        if "Output" in row: row["Output"] = row["Output"].replace("readable_intent_missing", "owner_conflict")
                if mutation == "panic": rows.append({"Output": "panic: fixture"})
                if mutation == "skip": rows[1]["Action"] = "skip"
                if mutation == "build": rows = [{"Output": "[build failed]"}]
                if mutation == "unrelated": rows[0]["Test"] = "TestOther"
                with self.assertRaises(ValueError):
                    EVIDENCE.validate("RED", 1, "\n".join(map(json.dumps, rows)))

    def test_partial_green_rejected(self):
        rows = fixture("GREEN")
        rows = [row for row in rows if row.get("Test") != EVIDENCE.NEXT + "/ctx_pool"]
        with self.assertRaises(ValueError):
            EVIDENCE.validate("GREEN", 0, "\n".join(map(json.dumps, rows)))


if __name__ == "__main__":
    unittest.main()
