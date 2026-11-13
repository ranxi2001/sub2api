"""Run only in remote CI; exercise accepted evidence and poisoned transcripts."""

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("assert-readable-continuation.py")
SPEC = importlib.util.spec_from_file_location("readable_evidence", SCRIPT)
GATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GATE)


def event(action, name=None, output=None):
    value = {"Action": action, "Package": GATE.PACKAGE}
    if name is not None:
        value["Test"] = name
    if output is not None:
        value["Output"] = output
    return value


def evidence(phase):
    terminal = "fail" if phase == "RED" else "pass"
    events = [event("run", GATE.ROOT)]
    for name in sorted(GATE.LEAVES):
        events.append(event("run", name))
        if phase == "RED":
            events.append(event("output", name, "response: encrypted_agent_recovery_wire_changed\n"))
        events.append(event(terminal, name))
    events.extend([event(terminal, GATE.ROOT), event(terminal)])
    return events


def transcript(events):
    return "".join(json.dumps(value) + "\n" for value in events)


class ReadableEvidenceTest(unittest.TestCase):
    def test_valid_red_and_green(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            with self.subTest(phase=phase):
                GATE.validate(phase, code, transcript(evidence(phase)))

    def test_raw_and_json_output_pollution_in_both_phases(self):
        diagnostics = (
            "panic: unexpected nil pointer",
            'openai.usage_record_task_panic_recovered {"panic":"nil pointer"}',
            "WARNING: DATA RACE",
            "race detected during execution of test",
            "fatal error: concurrent map writes",
            "runtime error: invalid memory address",
            "runtime: out of memory",
            "SIGSEGV: segmentation violation",
            "signal: killed",
            "Killed",
            "test timed out after 5m0s",
            "process crash: exit status 2",
            "FAIL [build failed]",
            "undefined: omit409SettingsRepo",
            "[no tests to run]",
            "--- SKIP: TestOpenAI409ReadableContinuationHTTP",
        )
        for phase, code in (("RED", 1), ("GREEN", 0)):
            for diagnostic in diagnostics:
                for encoded in (False, True):
                    with self.subTest(phase=phase, diagnostic=diagnostic, encoded=encoded):
                        poison = transcript([event("output", GATE.ROOT, diagnostic + "\n")]) if encoded else diagnostic + "\n"
                        with self.assertRaisesRegex(ValueError, "contaminated"):
                            GATE.validate(phase, code, transcript(evidence(phase)) + poison)

    def test_split_diagnostic_and_skip_event(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            for poison in (
                [event("output", GATE.ROOT, "WARNING: DATA "), event("output", GATE.ROOT, "RACE\n")],
                [event("skip", GATE.ROOT)],
            ):
                with self.subTest(phase=phase, poison=poison):
                    with self.assertRaises(ValueError):
                        GATE.validate(phase, code, transcript(evidence(phase) + poison))

    def test_missing_or_misattributed_red_diagnostics(self):
        for name in sorted(GATE.LEAVES):
            if not name.endswith("/completed"):
                continue
            for misattribute in (False, True):
                with self.subTest(name=name, misattribute=misattribute):
                    values = evidence("RED")
                    values = [value for value in values if not (value["Action"] == "output" and value.get("Test") == name)]
                    if misattribute:
                        values.append(event("output", GATE.ROOT, "encrypted_agent_recovery_wire_changed"))
                    with self.assertRaisesRegex(ValueError, "missing named input-integrity"):
                        GATE.validate("RED", 1, transcript(values))

    def test_missing_run_or_terminal_and_wrong_exit(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            values = evidence(phase)
            leaf_terminal = next(index for index, value in enumerate(values) if value["Action"] in {"pass", "fail"} and value.get("Test") in GATE.LEAVES)
            for index in (0, 1, leaf_terminal, len(values) - 2, len(values) - 1):
                with self.subTest(phase=phase, missing=index):
                    with self.assertRaises(ValueError):
                        GATE.validate(phase, code, transcript(values[:index] + values[index + 1:]))
            with self.subTest(phase=phase, wrong_exit=True):
                with self.assertRaises(ValueError):
                    GATE.validate(phase, 2, transcript(values))

    def test_unrelated_failure_cannot_hide_in_red(self):
        values = evidence("RED") + [event("fail", "TestUnrelatedFailure")]
        with self.assertRaisesRegex(ValueError, "unrelated failure"):
            GATE.validate("RED", 1, transcript(values))

    def test_cli_exit_status_for_valid_and_contaminated_logs(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "evidence.jsonl"
            for phase, code in (("RED", 1), ("GREEN", 0)):
                for poisoned in (False, True):
                    with self.subTest(phase=phase, poisoned=poisoned):
                        path.write_text(transcript(evidence(phase)) + ("panic: injected\n" if poisoned else ""))
                        result = subprocess.run(
                            [sys.executable, str(SCRIPT), phase, str(code), str(path)],
                            capture_output=True, text=True, timeout=5, check=False,
                        )
                        self.assertEqual(result.returncode, int(poisoned), result.stdout + result.stderr)
                        clean = subprocess.run(
                            [sys.executable, str(SCRIPT), "--clean-log", str(path)],
                            capture_output=True, text=True, timeout=5, check=False,
                        )
                        self.assertEqual(clean.returncode, int(poisoned), clean.stdout + clean.stderr)


if __name__ == "__main__":
    unittest.main()
