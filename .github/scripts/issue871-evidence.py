"""Accept only the named diagnosis assertion as the remote behavioral RED."""

import json
import sys
from pathlib import Path

PACKAGE = "github.com/Wei-Shaw/sub2api/internal/service"
ROOT = "TestExcelBPSSearchDiagnosticsThroughForward"
LEAF = ROOT + "/search_403/json"
NODES = {ROOT, LEAF}
MARKER = "issue871_expected_search_diagnosis"
ASSERTION = 'does not contain "phase=search_http; reason=http_rejected"'


def validate(phase, status, raw, stderr=""):
    if phase not in {"RED", "GREEN"} or status != (1 if phase == "RED" else 0):
        raise ValueError("unexpected phase or test exit status")
    combined = (raw + stderr).lower()
    for forbidden in (
        "panic:", "fatal error", "data race", "build failed", "build-fail",
        "setup failed", "no tests to run", "no test files", "--- skip:",
        "signal:", "test timed out",
    ):
        if forbidden in combined:
            raise ValueError("invalid behavioral evidence: " + forbidden)
    events = [json.loads(line) for line in raw.splitlines() if line.strip()]
    runs = {name: 0 for name in NODES}
    terminals = {name: [] for name in NODES}
    packages = []
    output = ""
    for event in events:
        if not isinstance(event, dict) or event.get("Package") != PACKAGE:
            raise ValueError("unexpected event/package")
        action, test = event.get("Action"), event.get("Test")
        if action not in {"start", "run", "pause", "cont", "output", "pass", "fail"}:
            raise ValueError("skip/build/unknown event")
        if test:
            if test not in NODES:
                raise ValueError("unexpected test: " + test)
            if action == "run":
                runs[test] += 1
            if action in {"pass", "fail"}:
                terminals[test].append(action)
            if action == "output" and test == LEAF:
                output += event.get("Output", "")
        elif action in {"pass", "fail"}:
            packages.append(action)
    expected = "fail" if phase == "RED" else "pass"
    if runs != {name: 1 for name in NODES}:
        raise ValueError("missing or repeated named test execution")
    if terminals != {name: [expected] for name in NODES} or packages != [expected]:
        raise ValueError("unexpected test/package terminal")
    if phase == "RED" and (output.count(MARKER) != 1 or ASSERTION not in output):
        raise ValueError("RED did not fail at the intended diagnosis assertion")


if __name__ == "__main__":
    phase, status, log, stderr = sys.argv[1:]
    validate(phase, int(status), Path(log).read_text(), Path(stderr).read_text())
    print(phase + " named search diagnosis evidence accepted")
