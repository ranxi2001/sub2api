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
NORMALIZED = "TestSSEQuotaNormalizedExplicitCodeReaders"
POOL429 = "TestSSEQuotaNormalizedPool429Readers"
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


def normalized_quota_leaves():
    return {
        f"{NORMALIZED}/{kind}/{quota}/{variant}/passthrough={passthrough}/pool502=true"
        for kind, quota, variant, passthrough in itertools.product(
            ("oauth", "apikey"), QUOTAS, ("uppercase", "padded"), BOOLS
        )
    }


def pool429_quota_leaves(kinds=("oauth", "apikey")):
    return {
        f"{POOL429}/{kind}/{quota}/{variant}/passthrough={passthrough}/pool={policy}"
        for kind, quota, variant, passthrough, policy in itertools.product(
            kinds, QUOTAS, ("uppercase", "padded"), BOOLS, ("default", "explicit429")
        )
    }


def green_tests():
    service = quota_leaves() | normalized_quota_leaves() | pool429_quota_leaves() | {
        READERS,
        NORMALIZED,
        POOL429,
        "TestSSEQuotaBareErrorCanComplete",
        "TestSSEQuotaClassificationRetainsExplicitCodeAndIntegrity",
        "TestSSEQuotaNormalizedAfterOutputCannotReplay",
        "TestSSEQuotaTerminalDoesNotGrantHTTPRejectionProof",
        "TestStrictSameAccountRetryRequiresCurrentBinding",
        "TestSameAccountRetrySchedulerCannotEscape",
        "TestStrictSessionAffinityLosingFirstTurnReleasesSlot",
        "TestOAuthNormalEOFBeforeOutputPreservesAccount",
        "TestOAuthNormalEOFAfterOutputCannotReplay",
        "TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ClientDisconnectStillDrainsUpstream",
        "TestOpenAIResponseFlush_BareErrorFollowedByCompletedUsesCompletedTerminal",
        "TestOpenAIStreamingPassthroughBareErrorTerminatesBeforeDone",
        "TestOpenAIStreamingPassthroughBareErrorDrainsAuthoritativeFailedUsage",
    }
    service.update(f"TestSSEQuotaBareErrorCanComplete/{quota}" for quota in QUOTAS)
    service.update(
        f"TestSSEQuotaNormalizedAfterOutputCannotReplay/{kind}/{quota}/{variant}/passthrough={passthrough}"
        for kind, quota, variant, passthrough in itertools.product(
            ("oauth", "apikey"), QUOTAS, ("uppercase", "padded"), BOOLS
        )
    )
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
    handler_red = mode in ("red-b1", "red-backoff")
    packages = {PKG + "service", PKG + "handler"} if mode == "green" else {PKG + ("handler" if handler_red else "service")}
    assert all(row.get("Package") in packages for row in rows), "unexpected package or compiler event"
    assert all(row.get("Action") in ("start", "run", "output", "pass", "fail", "pause", "cont") for row in rows), "unexpected Go event"
    counts = Counter((row.get("Package"), row.get("Test"), row.get("Action")) for row in rows)
    fragments = defaultdict(str)
    for row in rows:
        fragments[(row.get("Package"), row.get("Test"))] += row.get("Output", "")
    failed = {(row.get("Package"), row["Test"]) for row in rows if row.get("Action") == "fail" and row.get("Test")}
    passing_controls = set()
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
        assert mode in ("red-b1", "red-b3", "red-f1", "red-f2", "red-backoff"), mode
        package = PKG + ("handler" if handler_red else "service")
        names = {WS + ("/handshake" if mode == "red-b1" else "/backoff")} if handler_red else quota_leaves()
        if mode == "red-f1":
            names = normalized_quota_leaves()
        if mode == "red-f2":
            names = pool429_quota_leaves()
        expected = {(package, name) for name in names}
        terminal = "fail"
        # Slash-containing names passed to one t.Run do not create parent tests.
        expected_failures = expected | {(package, WS if handler_red else READERS)}
        if mode == "red-f1":
            expected_failures = expected | {(package, NORMALIZED)}
        if mode == "red-f2":
            # OAuth never enters pool mode. At the frozen F2 revision the
            # OAuth quota classifier already vetoed these 24 retries.
            passing_controls = {(package, name) for name in pool429_quota_leaves(("oauth",))}
            expected_failures = (expected - passing_controls) | {(package, POOL429)}
        assert failed == expected_failures, f"missing or unexpected RED assertions: {failed ^ expected_failures}"
        assert counts[(package, None, "fail")] == 1, "missing package failure"
        assert counts[(package, None, "pass")] == 0, "unexpected package PASS"
        assert not re.search(r"context deadline exceeded|runtime error:|panic_recovered|signal:", output, re.I), "runtime/setup failure is not RED"
        for key in expected:
            observed = fragments[key]
            if handler_red:
                stage = "handshake" if mode == "red-b1" else "backoff"
                assert f"retry deadline observation: stage={stage} generations=2" in observed, key
                assert "retry deadline: no generation may start after D" in observed, key
                if mode == "red-backoff":
                    assert "retry deadline fallback observation: stage=backoff second_after_deadline=true owners=[Bearer fixture-deadline-owner Bearer fixture-healthy-backup]" in observed, key
                wanted, actual = 1, 2
            elif mode == "red-f2":
                retry = "false" if key in passing_controls else "true"
                assert f"SSE pool quota observation: semantic_status=429 retry={retry} deadline_zero=true rejection_proof=false output_bytes=0 retry_started=false raw_preserved=true" in observed, key
                if key in passing_controls:
                    continue
                assert "terminal normalized quota must veto same-account pool retry" in observed, key
                assert re.search(r"Error:\s+Should be false", observed), key
                assert len(re.findall(r"Error Trace:", observed)) == 1, key
                continue
            elif mode == "red-f1":
                assert "SSE normalized quota observation:" in observed, key
                assert "semantic_status=502" in observed, key
                assert "terminal normalized explicit quota must preserve semantic 429" in observed, key
                wanted, actual = 429, 502
            else:
                assert "semantic_status=502" in observed, key
                assert "terminal type-only quota must preserve semantic 429" in observed, key
                wanted, actual = 429, 502
            assert re.search(rf"expected:\s+{wanted}\b", observed), key
            assert re.search(rf"actual\s*:\s+{actual}\b", observed), key
    for package, name in expected:
        assert counts[(package, name, "run")] == 1, f"missing/duplicate RUN: {name}"
        outcome = "pass" if (package, name) in passing_controls else terminal
        assert counts[(package, name, outcome)] == 1, f"missing/duplicate {outcome}: {name}"
        assert sum(counts[(package, name, action)] for action in ("pass", "fail", "skip")) == 1, name
    print(f"{mode}: verified {len(expected)} exact named executions")


if __name__ == "__main__":
    verify(*sys.argv[1:])
