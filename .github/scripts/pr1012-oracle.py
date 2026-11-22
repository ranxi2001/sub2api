#!/usr/bin/env python3
"""Verify named PR1012 behavior in go test JSONL; exit status alone is not RED."""

import itertools
import json
import re
import sys
from collections import Counter, defaultdict
from pathlib import Path


PKG = "github.com/Wei-Shaw/sub2api/internal/"
WS = "TestOpenAIWSRetryDeadlineAtGenerationWrite"
READERS = "TestSSETypeOnlyQuotaReaders"
QUOTAS = ("insufficient_quota", "quota_exceeded", "usage_limit_reached")
BOOLS = ("false", "true")
STAGES = ("backoff", "user_slot", "account_slot", "handshake", "inflight", "output", "canceled")


def quota_leaves():
    return {
        f"{READERS}/{kind}/{quota}/passthrough={passthrough}/pool502={pool}"
        for kind, quota, passthrough, pool in itertools.product(
            ("oauth", "apikey"), QUOTAS, BOOLS, BOOLS
        )
    }


def green_tests():
    service = quota_leaves() | {
        READERS,
        "TestSSEQuotaBareErrorCanComplete",
        "TestSSEQuotaClassificationRetainsExplicitCodeAndIntegrity",
        "TestSSEQuotaTerminalDoesNotGrantHTTPRejectionProof",
        "TestStrictSameAccountRetryRequiresCurrentBinding",
        "TestSameAccountRetrySchedulerCannotEscape",
        "TestStrictSessionAffinityLosingFirstTurnReleasesSlot",
        "TestOAuthNormalEOFBeforeOutputPreservesAccount",
        "TestOAuthNormalEOFAfterOutputCannotReplay",
        "TestOpenAIResponseFlush_BareErrorFollowedByCompletedUsesCompletedTerminal",
        "TestOpenAIStreamingPassthroughBareErrorTerminatesBeforeDone",
        "TestOpenAIStreamingPassthroughBareErrorDrainsAuthoritativeFailedUsage",
    }
    service.update(f"TestSSEQuotaBareErrorCanComplete/{quota}" for quota in QUOTAS)
    service.update(f"TestStrictSameAccountRetryRequiresCurrentBinding/{owner}" for owner in ("947101", "947102", "0"))
    service.update(
        f"TestSameAccountRetrySchedulerCannotEscape/advanced={advanced}/weighted={weighted}/{scenario}"
        for advanced, weighted, scenario in itertools.product(
            BOOLS, BOOLS, ("busy", "recovered", "excluded", "disabled", "wrong_group", "no_session", "expired", "canceled")
        )
    )
    handler = {WS, "TestSSEQuotaWithoutResetUsesBoundedAccountFailover", "TestOAuthRetryOwnerBusyAfterSlotRelease", "TestIssue917ResponsesCooldown"}
    handler.update(f"{WS}/{stage}" for stage in STAGES)
    handler.update(
        f"TestSSEQuotaWithoutResetUsesBoundedAccountFailover/{kind}/{form}/passthrough={passthrough}/stream={stream}"
        for kind, form, passthrough, stream in itertools.product(
            ("oauth", "apikey"), QUOTAS + tuple(f"type:{quota}" for quota in QUOTAS), BOOLS, BOOLS
        )
    )
    handler.update(
        f"TestOAuthRetryOwnerBusyAfterSlotRelease/{scenario}/switches={switches}"
        for scenario, switches in itertools.product(("busy_timeout", "recovered", "exhausted", "canceled"), (0, 2))
    )
    handler.update(
        f"TestIssue917ResponsesCooldown/{route}/{case}"
        for route, case in itertools.product(
            ("normal", "passthrough", "oauth"),
            ("initial", "after_output", "disabled", "nonmatching", "legacy_timing", "before_stream", "threshold_eof", "threshold_pair_before_output", "threshold_pair_after_output", "context_window", "encrypted_request", "cyber_policy"),
        )
    )
    return {(PKG + "service", name) for name in service} | {(PKG + "handler", name) for name in handler}


def verify(mode, status_path, log_path):
    status = int(Path(status_path).read_text().strip())
    rows = []
    for line in Path(log_path).read_text().splitlines():
        if not line.strip() or line.startswith("go: downloading "):
            continue
        rows.append(json.loads(line))
    assert rows, "missing Go test execution"
    output = "".join(row.get("Output", "") for row in rows)
    forbidden = r"WARNING: DATA RACE|race detected during execution of test|panic:|fatal error:|test timed out|build failed|setup failed|undefined:|syntax error:|no tests to run|signal: killed|fixture read timed out|handler did not release the retry|upstream retry lease remained open|retry handshake was not observed|retry never reached the held handshake"
    assert not re.search(forbidden, output, re.I), "non-behavior failure in evidence"
    assert not any(row.get("Action") in ("skip", "build-fail") for row in rows), "skipped/build-failed execution"
    packages = {PKG + "service", PKG + "handler"} if mode == "green" else {PKG + ("handler" if mode == "red-b1" else "service")}
    assert all(row.get("Package") in packages for row in rows), "unexpected package or compiler event"
    assert all(row.get("Action") in ("start", "run", "output", "pass", "fail", "pause", "cont") for row in rows), "unexpected Go event"
    counts = Counter((row.get("Package"), row.get("Test"), row.get("Action")) for row in rows)
    fragments = defaultdict(str)
    for row in rows:
        fragments[(row.get("Package"), row.get("Test"))] += row.get("Output", "")
    failed = {(row.get("Package"), row["Test"]) for row in rows if row.get("Action") == "fail" and row.get("Test")}
    if mode == "green":
        assert status == 0, f"GREEN exit {status}"
        expected = green_tests()
        terminal = "pass"
        assert not failed, f"unexpected GREEN failures: {failed}"
        for package in (PKG + "service", PKG + "handler"):
            assert counts[(package, None, "pass")] == 1, f"missing package PASS: {package}"
            assert counts[(package, None, "fail")] == 0, f"package failed: {package}"
    else:
        assert status == 1, f"RED must be a test failure, got {status}"
        assert mode in ("red-b1", "red-b3"), mode
        package = PKG + ("handler" if mode == "red-b1" else "service")
        names = {WS + "/handshake"} if mode == "red-b1" else quota_leaves()
        expected = {(package, name) for name in names}
        terminal = "fail"
        # Slash-containing names passed to one t.Run do not create parent tests.
        expected_failures = expected | {(package, WS if mode == "red-b1" else READERS)}
        assert failed == expected_failures, f"missing or unexpected RED assertions: {failed ^ expected_failures}"
        assert counts[(package, None, "fail")] == 1, "missing package failure"
        assert counts[(package, None, "pass")] == 0, "unexpected package PASS"
        assert not re.search(r"context deadline exceeded|runtime error:|panic_recovered|signal:", output, re.I), "runtime/setup failure is not RED"
        for key in expected:
            observed = fragments[key]
            if mode == "red-b1":
                assert "retry deadline observation: stage=handshake generations=2" in observed, key
                assert "retry deadline: no generation may start after D" in observed, key
                wanted, actual = 1, 2
            else:
                assert "semantic_status=502" in observed, key
                assert "terminal type-only quota must preserve semantic 429" in observed, key
                wanted, actual = 429, 502
            assert re.search(rf"expected:\s+{wanted}\b", observed), key
            assert re.search(rf"actual\s*:\s+{actual}\b", observed), key
    for package, name in expected:
        assert counts[(package, name, "run")] == 1, f"missing/duplicate RUN: {name}"
        assert counts[(package, name, terminal)] == 1, f"missing/duplicate {terminal}: {name}"
        assert sum(counts[(package, name, action)] for action in ("pass", "fail", "skip")) == 1, name
    print(f"{mode}: verified {len(expected)} exact named executions")


if __name__ == "__main__":
    verify(*sys.argv[1:])
