"""Small synthetic log checks; these do not execute or stand in for Go tests."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class OracleGateTest(unittest.TestCase):
    oracle = "TestPR899IndependentVertexAPIKeyMustNotConvertToUnsupportedAnthropicAuth"
    control = "TestPR899VertexServiceAccountJSONRemainsPortable"
    package = "github.com/Wei-Shaw/sub2api/internal/service"
    script = Path(__file__).with_name("pr899-assert-oracle.py")

    def event(self, action, test=None, output=None):
        event = {"Action": action, "Package": self.package}
        if test:
            event["Test"] = test
        if output:
            event["Output"] = output
        return event

    def valid(self, mode):
        events = [self.event("pass", self.control + "/" + str(i)) for i in range(8)]
        events.append(self.event("pass", self.control))
        if mode == "red":
            events.append(self.event("output", self.oracle,
                                     "An error is expected but got nil\n"
                                     "Anthropic Vertex only supports Service Account JSON"))
        result = "fail" if mode == "red" else "pass"
        return events + [self.event(result, self.oracle), self.event(result)]

    def check(self, mode, events, status, accepted=False, stderr=""):
        with tempfile.TemporaryDirectory() as directory:
            log, err = Path(directory) / "test.jsonl", Path(directory) / "test.stderr"
            log.write_text("".join(json.dumps(event) + "\n" for event in events))
            err.write_text(stderr)
            result = subprocess.run(
                [sys.executable, str(self.script), mode, str(log), str(err), str(status)],
                capture_output=True, text=True, timeout=10,
            )
        self.assertEqual(result.returncode == 0, accepted, result.stdout + result.stderr)

    def test_accepts_completed_business_results(self):
        for mode, status in (("red", 1), ("green", 0)):
            with self.subTest(mode=mode):
                self.check(mode, self.valid(mode), status, accepted=True)

    def test_rejects_contamination_even_after_expected_terminals(self):
        markers = (
            "panic: test timed out after 10m0s", "panic: unexpected nil pointer",
            "fatal error: concurrent map writes", "runtime: out of memory",
            "WARNING: DATA RACE", "race detected during execution of test",
            "SIGSEGV: segmentation violation", "signal: killed", "Aborted (core dumped)",
            "unexpected fault address 0x0", "test binary crashed", "exit status 137",
            "AddressSanitizer: heap-use-after-free", "runtime/cgo: pthread_create failed",
        )
        for mode, status in (("red", 1), ("green", 0)):
            for marker in markers:
                for stream in ("json", "stderr"):
                    with self.subTest(mode=mode, marker=marker, stream=stream):
                        events, stderr = self.valid(mode), ""
                        if stream == "json":
                            events.append(self.event("output", output=marker))
                        else:
                            stderr = marker
                        self.check(mode, events, status, stderr=stderr)

    def test_rejects_incomplete_or_wrong_results(self):
        cases = {
            "build_failure": [self.event("build-fail")],
            "no_tests": [self.event("fail")],
            "missing_control": self.valid("red")[1:],
            "missing_package_terminal": self.valid("red")[:-1],
            "skipped": [self.event("skip", "OtherTest")] + self.valid("red"),
            "unrelated_failure": self.valid("red") + [self.event("fail", "OtherTest")],
            "missing_assertion": [event for event in self.valid("red") if event["Action"] != "output"],
        }
        for name, events in cases.items():
            with self.subTest(name=name):
                self.check("red", events, 1)
        for status in (0, 2, 124, 137, 139, 143):
            with self.subTest(status=status):
                self.check("red", self.valid("red"), status)


if __name__ == "__main__":
    unittest.main()
