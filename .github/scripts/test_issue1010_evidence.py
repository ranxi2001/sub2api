"""Offline evidence-validator regressions; executed only in remote CI."""

import json
import unittest

from issue1010_evidence import ASSERTION, GUARD, LEAVES, NODES, PACKAGE, validate


def oracle_events(mode):
    terminal = "fail" if mode == "red" else "pass"
    events = [{"Action": "start", "Package": PACKAGE}]
    for name in sorted(NODES):
        events.append({"Action": "run", "Package": PACKAGE, "Test": name})
        if name in LEAVES and mode == "red":
            events.append({"Action": "output", "Package": PACKAGE, "Test": name,
                           "Output": f"{ASSERTION}: {GUARD}\n"})
        events.append({"Action": terminal, "Package": PACKAGE, "Test": name})
    events.append({"Action": terminal, "Package": PACKAGE})
    return events


def encode(events):
    return "\n".join(json.dumps(event) for event in events)


class TestIssue1010RedEvidenceRejectsMixedGuardAndSetupFailures(unittest.TestCase):
    def test_all_eight_expected_failures_and_passes(self):
        for mode in ("red", "green"):
            with self.subTest(mode=mode):
                validate(encode(oracle_events(mode)), mode)

    def test_each_leaf_requires_its_own_assertion_and_guard(self):
        for leaf in sorted(LEAVES):
            for replacement in ("unrelated assertion", ASSERTION, GUARD):
                with self.subTest(leaf=leaf, replacement=replacement):
                    events = oracle_events("red")
                    for event in events:
                        if event.get("Test") == leaf and event["Action"] == "output":
                            event["Output"] = replacement
                    with self.assertRaises(ValueError):
                        validate(encode(events), "red")

    def test_setup_build_skip_crash_race_and_missing_or_unexpected_failures(self):
        leaf = sorted(LEAVES)[0]
        for mode in ("red", "green"):
            for defect in ("setup failed", "[build failed]", "panic: fixture", "WARNING: DATA RACE",
                           "skip", "missing", "extra-fail", "wrong-outcome", "duplicate"):
                with self.subTest(mode=mode, defect=defect):
                    events = oracle_events(mode)
                    if defect == "missing":
                        events = [event for event in events if event.get("Test") != leaf]
                    elif defect == "skip":
                        events.append({"Action": "skip", "Package": PACKAGE, "Test": leaf})
                    elif defect == "extra-fail":
                        events.append({"Action": "fail", "Package": PACKAGE, "Test": "UnexpectedSetup"})
                    elif defect == "wrong-outcome":
                        for event in events:
                            if event.get("Test") == leaf and event["Action"] in ("pass", "fail"):
                                event["Action"] = "pass" if mode == "red" else "fail"
                    elif defect == "duplicate":
                        events.append({"Action": "run", "Package": PACKAGE, "Test": leaf})
                    else:
                        events.append({"Action": "output", "Package": PACKAGE, "Test": leaf,
                                       "Output": defect})
                    with self.assertRaises(ValueError):
                        validate(encode(events), mode)


if __name__ == "__main__":
    unittest.main()
