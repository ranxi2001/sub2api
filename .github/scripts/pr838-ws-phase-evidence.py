"""Validate completed remote forwarding regressions; infrastructure is never RED."""
import json
from pathlib import Path
import sys

mode, filename, status = sys.argv[1:]
path = Path(filename)
events = [json.loads(line) for line in path.read_text().splitlines()]
output = "".join(event.get("Output", "") for event in events) + path.with_suffix(".stderr").read_text()
path.with_suffix(".log").write_text(output)
print(output)
assert events and not any(event["Action"] == "skip" for event in events), "missing execution or skipped regression"
assert not any(term in output.lower() for term in (
    "build failed", "setup failed", "undefined:", "no tests to run", "panic:",
    "fatal error:", "warning: data race", "race detected", "signal:",
    "segmentation fault", "test timed out", "unexpected fault", "out of memory",
)), "compilation, race, crash or infrastructure cannot satisfy behavior evidence"
started = {event["Test"] for event in events if event["Action"] == "run" and event.get("Test")}
finished = {event["Test"] for event in events if event["Action"] in ("pass", "fail") and event.get("Test")}
assert started and started == finished, "unfinished forwarding tests"
passed = {event.get("Test") for event in events if event["Action"] == "pass"}
failed = {event.get("Test") for event in events if event["Action"] == "fail"}
terminal = [event for event in events if not event.get("Test") and event["Action"] in ("pass", "fail")]
assert len(terminal) == 1, "missing completed package result"
root = "TestTempUnschedWSForwardAfterOutputSemantic429/"
for entry in ("v2", "ingress"):
    assert root + entry + "/after_stream" in passed, "after_stream control missing: " + entry
    if mode == "red":
        assert root + entry + "/before_stream" in failed, "old source must fail the actual forwarding entry: " + entry
    else:
        assert root + entry + "/before_stream" in passed, "candidate failed the actual forwarding entry: " + entry
if mode == "red":
    assert int(status) == 1 and terminal[0]["Action"] == "fail"
    assert "semantic429 must respect the real output phase and count each response once" in output
else:
    assert int(status) == 0 and not failed and terminal[0]["Action"] == "pass"
    for name in (
        "TestCompatErrorPipelineCountsEachResponseOnce",
        "TestFastPathThenGeneralHandler_CountsOne502UntilThreshold",
        "TestPendingModel401_DoesNotDisableWholeAccount",
        "TestCredential401_StillDisablesDespitePendingModelRule",
        "TestOpenAIWSAfterOutput_Generic500AndExplicit502NeedAfterStream",
        "TestMemoryTempUnschedTriggerDelayedResetKeepsNewRound",
        "TestOAuth429WeeklyOnlyService", "TestOAuth429WeeklyOnlyWSForward",
        "TestOAuth429WeeklyOnlyWSClientIngress",
    ):
        assert name in passed, "missing safety control: " + name
print(f"Validated WS PHASE {mode.upper()}: {len(passed)} passed events, {len(failed)} failed events")
