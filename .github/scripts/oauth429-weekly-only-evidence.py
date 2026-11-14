"""Validate completed remote Go test evidence, rejecting crashes and stderr errors."""
import json
from pathlib import Path
import sys

mode, filename, status = sys.argv[1:]
path = Path(filename)
events = [json.loads(line) for line in path.read_text().splitlines()]
stderr = path.with_suffix(".stderr").read_text()
output = "".join(event.get("Output", "") for event in events)
path.with_suffix(".log").write_text(output + stderr)
print(output + stderr)
assert events, "missing test events"
assert not any(event["Action"] == "skip" for event in events), "unexpected skip"
combined = (output + stderr).lower()
assert not any(text in combined for text in (
    "build failed", "setup failed", "no tests to run", "undefined:", "panic:",
    "fatal error:", "warning: data race", "race detected", "signal:",
    "sigsegv", "sigabrt", "segmentation fault", "test timed out", "unexpected fault",
    "out of memory", "signal killed", "core dumped",
)), "runtime, race, infrastructure or compilation failure is not behavioral evidence"
started = {(event.get("Package"), event["Test"]) for event in events
           if event["Action"] == "run" and event.get("Test")}
finished = {(event.get("Package"), event["Test"]) for event in events
            if event["Action"] in ("pass", "fail") and event.get("Test")}
assert started and started == finished, "test execution did not finish"
package = "github.com/Wei-Shaw/sub2api/internal/" + ("repository" if mode in ("repository", "red-query") else "service")
terminal = [event for event in events if event.get("Package") == package
            and not event.get("Test") and event["Action"] in ("pass", "fail")]
assert len(terminal) == 1, "missing completed package result"
failed = {event.get("Test") for event in events if event["Action"] == "fail"}
passed = {event.get("Test") for event in events if event["Action"] == "pass"}
if mode.startswith("red"):
    assert int(status) == 1 and terminal[0]["Action"] == "fail", "expected normal go test assertion failure"
    if mode == "red-query":
        for name in ("new_weekly_zero", "new_weekly_low"):
            assert "TestOAuth429WeeklyOnlyQueryUsageSupersedesEvidence/" + name in failed
        assert "TestOAuth429WeeklyOnlyQueryUsageSupersedesEvidence/older_query_cannot_replace_newer_high" in passed
        assert "TestOAuth429WeeklyOnlyQueryUsageSupersedesEvidence/new_weekly_high" in passed
        assert "actual QueryUsage persistence must supersede older weekly high" in output
    elif mode == "red-http-expiry":
        for name in ("native_stream", "passthrough_stream", "native_buffered", "passthrough_buffered", "ws_http_bridge"):
            test = "TestOAuth429WeeklyOnlyHTTPStreamExpiredObservation/" + name
            assert test in failed, name
            assert any(event.get("Test") == test and "expired HTTP response quota must not permanently disable" in event.get("Output", "") for event in events), "wrong expiry failure: " + name
    elif mode == "red-cache":
        assert "TestOAuth429WeeklyOnlySequentialCache/duplicate_usage" in failed
        assert "TestOAuth429WeeklyOnlySequentialCache/nan_weekly_usage" in failed
        assert "TestOAuth429WeeklyOnlyNonweeklyPersistenceRetainsEvidence" in failed
        assert "rejected weekly headers must not poison a later headerless 429 cache" in output
    else:
        for name in ("primary_weekly_zero_neighbor", "secondary_weekly_invalid_neighbor"):
            assert "TestOAuth429WeeklyOnlyService/" + name in failed, name
        for name in ("five_hour_100_zero_neighbor", "five_hour_100_invalid_neighbor"):
            assert "TestOAuth429WeeklyOnlyNonweeklyHeadersAllowExplicitCache/" + name in failed, name
        assert "nonweekly headers must not veto fresh explicit weekly cache" in output
        assert "explicit weekly 99 must disable despite an unrelated neighbor" in output
        assert "TestOAuth429WeeklyOnlyService/primary_weekly_five_hour_neighbor" in passed
        for name in (
            "HTTPForward/native/primary_zero_neighbor",
            "HTTPForward/passthrough/secondary_zero_neighbor",
            "WSForward/handshake_weekly_zero_neighbor",
            "WSForward/frame_with_weekly_handshake",
            "WSForward/frame_low_overrides_high_cache",
            "WSForward/frame_invalid_overrides_high_cache",
            "WSForward/frame_elapsed_handshake_reset",
            "NonweeklyPersistenceRetainsEvidence",
            "WSClientIngress",
        ):
            assert "TestOAuth429WeeklyOnly" + name in failed, name
        assert "TestOAuth429WeeklyOnlyWSForward/body_only_no_percentage" in passed
        assert "TestOAuth429WeeklyOnlyHTTPStreamExpiredObservation" in passed
        assert "request admission denied:" not in output
else:
    assert int(status) == 0 and not failed and terminal[0]["Action"] == "pass", "candidate failed"
    required = ["RotationPreservesScheduling", "InvalidWeeklyPersistence", "NonweeklyPersistenceRetainsEvidence", "QueryUsageSupersedesEvidence"] if mode == "repository" else [
        "Service", "HeaderBoundaries", "CachedIdentity", "NonweeklyHeadersAllowExplicitCache",
        "CacheDoesNotRelabelUnidentifiedUsage", "SequentialCache", "NonweeklyPersistenceRetainsEvidence", "PolicyGates",
        "HTTPForward", "HTTPStreamExpiredObservation", "WSForward", "WSClientIngress", "CombinedRotation",
    ]
    for name in required:
        assert "TestOAuth429WeeklyOnly" + name in passed, "missing regression: " + name
    if mode == "green":
        for name in (
            "TestCompatErrorPipelineCountsEachResponseOnce",
            "TestFastPathThenGeneralHandler_CountsOne502UntilThreshold",
            "TestPendingModel401_DoesNotDisableWholeAccount",
            "TestCredential401_StillDisablesDespitePendingModelRule",
            "TestOpenAIWSAfterOutput_Generic500AndExplicit502NeedAfterStream",
            "TestMemoryTempUnschedTriggerDelayedResetKeepsNewRound",
        ):
            assert name in passed, "missing candidate-only regression: " + name
print(f"Validated {mode.upper()}: {len(passed)} passed events, {len(failed)} failed events")
