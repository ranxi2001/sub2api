"""Validate remote Go test events; never accept compilation failure as RED."""
import json
from pathlib import Path
import sys

mode, filename, status = sys.argv[1:]
events = [json.loads(line) for line in Path(filename).read_text().splitlines()]
output = "".join(event.get("Output", "") for event in events)
Path(filename).with_suffix(".log").write_text(output)
print(output)
assert events, "missing test events"
assert not any(event["Action"] == "skip" for event in events), "unexpected skip"
assert not any(text in output for text in (
    "build failed", "setup failed", "no tests to run", "undefined:", "panic:",
)), "infrastructure or compilation failure is not behavioral evidence"
failed = {event.get("Test") for event in events if event["Action"] == "fail"}
passed = {event.get("Test") for event in events if event["Action"] == "pass"}
if mode == "red":
    assert int(status) != 0, "baseline unexpectedly passed"
    for name in ("primary_weekly_zero_neighbor", "secondary_weekly_invalid_neighbor"):
        assert "TestOAuth429WeeklyOnlyService/" + name in failed, name
    assert "explicit weekly 99 must disable despite an unrelated neighbor" in output
    assert "TestOAuth429WeeklyOnlyService/primary_weekly_five_hour_neighbor" in passed
else:
    assert int(status) == 0 and not failed, "candidate failed"
    required = ["RotationPreservesScheduling"] if mode == "repository" else [
        "Service", "HeaderBoundaries", "CachedIdentity", "PolicyGates",
        "HTTPForward", "WSForward", "CombinedRotation",
    ]
    for name in required:
        assert "TestOAuth429WeeklyOnly" + name in passed, "missing regression: " + name
print(f"Validated {mode.upper()}: {len(passed)} passed events, {len(failed)} failed events")
