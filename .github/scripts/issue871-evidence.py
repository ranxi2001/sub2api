"""Accept only the named diagnosis or business assertion as behavioral RED."""

import json
import sys
from pathlib import Path

PACKAGE = "github.com/Wei-Shaw/sub2api/internal/service"
ROOT = "TestExcelBPSSearchDiagnosticsThroughForward"
LEAF = ROOT + "/search_403/json"
NODES = {ROOT, LEAF}
MARKER = "issue871_expected_search_diagnosis"
ASSERTION = 'does not contain "phase=search_http; reason=http_rejected"'
BUSINESS_ROOT = "TestExcelBPSSearchBusinessThroughForward"
BUSINESS_LEAF = BUSINESS_ROOT + "/direct/json"
BUSINESS_MARKER = "issue871_expected_native_search_history"
BUSINESS_ASSERTION = BUSINESS_MARKER + ': got "__sub2api_web_search"; want "run_officejs"'
PENDING_ROOT = "TestOpenAIToolProvenancePendingDeadline"
PENDING_LEAF = PENDING_ROOT + "/function/pending_deadline"
PENDING_MARKER = "WS_PENDING_DEADLINE_CLASSIFICATION"
PENDING_ASSERTION = PENDING_MARKER + ': got "encrypted_agent_recovery_store_unavailable"; want "encrypted_agent_recovery_owner_conflict"'


def validate(phase, status, raw, stderr="", contract="diagnosis"):
    if contract == "diagnosis":
        nodes, leaf, marker, assertion = NODES, LEAF, MARKER, ASSERTION
    elif contract == "business":
        nodes = {BUSINESS_ROOT, BUSINESS_LEAF}
        leaf, marker, assertion = BUSINESS_LEAF, BUSINESS_MARKER, BUSINESS_ASSERTION
    elif contract == "pending_deadline":
        nodes = {PENDING_ROOT, PENDING_LEAF}
        leaf, marker, assertion = PENDING_LEAF, PENDING_MARKER, PENDING_ASSERTION
    else:
        raise ValueError("unknown evidence contract")
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
    runs = {name: 0 for name in nodes}
    terminals = {name: [] for name in nodes}
    packages = []
    output = ""
    for event in events:
        if not isinstance(event, dict) or event.get("Package") != PACKAGE:
            raise ValueError("unexpected event/package")
        action, test = event.get("Action"), event.get("Test")
        if action not in {"start", "run", "pause", "cont", "output", "pass", "fail"}:
            raise ValueError("skip/build/unknown event")
        if test:
            if test not in nodes:
                raise ValueError("unexpected test: " + test)
            if action == "run":
                runs[test] += 1
            if action in {"pass", "fail"}:
                terminals[test].append(action)
            if action == "output" and test == leaf:
                output += event.get("Output", "")
        elif action in {"pass", "fail"}:
            packages.append(action)
    expected = "fail" if phase == "RED" else "pass"
    if runs != {name: 1 for name in nodes}:
        raise ValueError("missing or repeated named test execution")
    if terminals != {name: [expected] for name in nodes} or packages != [expected]:
        raise ValueError("unexpected test/package terminal")
    if phase == "RED" and (output.count(marker) != 1 or assertion not in output):
        raise ValueError("RED did not fail at the intended " + contract + " assertion")


if __name__ == "__main__":
    phase, status, log, stderr, *extra = sys.argv[1:]
    if len(extra) > 1:
        raise ValueError("expected at most one evidence contract")
    contract = extra[0] if extra else "diagnosis"
    validate(phase, int(status), Path(log).read_text(), Path(stderr).read_text(), contract)
    print(phase + " named search " + contract + " evidence accepted")
