#!/usr/bin/env python3
"""Require completed business assertions without build or runtime contamination."""
import json
import re
import sys
from pathlib import Path

mode, log, stderr_log, exit_code = sys.argv[1:]
assert mode in {"red", "green"}
events = [json.loads(line) for line in Path(log).read_text().splitlines() if line.strip()]
# A genuine assertion may appear before a later panic/race/crash. Check every
# output event and stderr, even when test/package terminals look as expected.
output_chunks = [event.get("Output", "") for event in events]
contamination = re.compile(
    r"panic:|fatal(?: error)?:|\bruntime:|\bruntime/cgo:|\bdata race\b|"
    r"race detected during execution of test|\bSIG(?:SEGV|ABRT|BUS|ILL|FPE|KILL|TERM|QUIT)\b|"
    r"\bsignal:\s*|segmentation fault|bus error|core dumped|"
    r"\b(?:AddressSanitizer|MemorySanitizer|ThreadSanitizer):|"
    r"unexpected fault address|\b(?:crash(?:ed)?|killed|aborted)\b|"
    r"\bexit status (?:[2-9]|[1-9][0-9]+)\b",
    re.IGNORECASE,
)
assert not any(contamination.search(output) for output in (
    "".join(output_chunks), "\n".join(output_chunks), Path(stderr_log).read_text()
)), "panic/race/crash/timeout output is not acceptance"
oracle = "TestPR899IndependentVertexAPIKeyMustNotConvertToUnsupportedAnthropicAuth"
control = "TestPR899VertexServiceAccountJSONRemainsPortable"
terminal = {event["Test"]: event["Action"] for event in events
            if event.get("Test") and event["Action"] in {"pass", "fail", "skip"}}
assert terminal.get(control) == "pass", "legal JSON control did not complete successfully"
control_cases = {test: result for test, result in terminal.items() if test.startswith(control + "/")}
assert len(control_cases) == 8 and set(control_cases.values()) == {"pass"}, control_cases
assert not any(event["Action"] == "skip" for event in events), "skips are not acceptance"
assert not any(event["Action"] == "build-fail" for event in events), "build failure is not RED"
packages = [event for event in events if not event.get("Test") and event["Action"] in {"pass", "fail", "skip"}]
assert len(packages) == 1 and packages[0]["Package"] == "github.com/Wei-Shaw/sub2api/internal/service", packages
assert packages[0]["Action"] == ("fail" if mode == "red" else "pass"), packages
failures = {test for test, result in terminal.items() if result == "fail"}
if mode == "red":
    assert int(exit_code) == 1, f"unexpected RED process exit: {exit_code}"
    assert failures == {oracle}, failures
    output = "".join(event.get("Output", "") for event in events if event.get("Test") == oracle)
    assert "An error is expected but got nil" in output, "missing real business assertion failure"
    assert "Anthropic Vertex only supports Service Account JSON" in output, "wrong failure oracle"
else:
    assert int(exit_code) == 0 and terminal.get(oracle) == "pass" and not failures, terminal
print(f"{mode.upper()}: expected oracle result and all 8 JSON controls completed")
