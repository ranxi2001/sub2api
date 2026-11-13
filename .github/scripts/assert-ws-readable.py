"""Named transport evidence for the next WS and OAuth/pointer increment."""

import importlib.util
import json
import sys
from collections import Counter
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("readable_common", Path(__file__).with_name("assert-readable-continuation.py"))
COMMON = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(COMMON)
MODES = ("http_bridge", "ctx_pool", "shared", "dedicated", "passthrough")
SUITES = {
    "ws": ("TestOpenAI409WSReadableContinuation", {mode for mode in MODES}),
    "ws-boundaries": ("TestOpenAI409WSReadableBoundaries", {f"{mode}/{case}" for mode in MODES for case in ("off", "no_intent", "b_disabled", "pending_owner", "failed_owner", "ambiguous_B")}),
    "oauth-shadow": ("TestOpenAI409FutureShadowOAuth", {f"stream={stream}/{case}" for stream in ("false", "true") for case in ("recover", "write_failed", "pending_owner", "store_read_failed", "two_known_tasks", "fresh_intent")}),
    "pointer": ("TestOpenAI409HTTPReadablePointerWithoutOpaque", {f"{account}/stream={stream}/{pointer}" for account in ("apikey", "oauth") for stream in ("false", "true") for pointer in ("previous", "conversation")}),
    "pointer-boundaries": ("TestOpenAI409HTTPPointerWithoutOpaqueBoundaries", {f"{account}/stream={stream}/{case}" for account in ("apikey", "oauth") for stream in ("false", "true") for case in ("strict", "pointer_only", "foreign_user", "b_disabled")}),
    "strict-pointer": ("TestOpenAI409HTTPPointerWithoutOpaqueBoundaries", {f"{account}/stream={stream}/strict" for account in ("apikey", "oauth") for stream in ("false", "true")}),
    "shadow-races": ("TestOpenAI409FutureShadowPublicationFences", {f"stream={stream}/{case}" for stream in ("false", "true") for case in ("first_publication", "invalidate_after_restore", "reused_exposure", "store_failure_existing", "delayed_publisher")}),
    "audit-provenance": ("TestOpenAI409FutureShadowAuditProvenance", {f"stream={stream}" for stream in ("false", "true")}),
    "audit-persistence": ("TestRecoveredContextAuditPersistence", {"blocking_allow", "blocking_risk", "async"}),
    "audit-async": ("TestRecoveredContextAsyncEnforcement", {"admission", "penalty"}),
    "ws-oracle": ("TestOpenAI409WSAmbiguousOracleMutations", {f"{transport}/{mutation}" for transport in ("http_bridge", "socket") for mutation in ("valid", "wrong_credential", "old_pointer", "missing_result", "unrelated_eof")}),
}
RED_SUITES = {"ws", "strict-pointer", "shadow-races", "audit-provenance", "audit-persistence", "audit-async"}


def diagnostic(suite, name):
    if suite == "ws":
        return ("WS_READABLE_B_DISPATCH_MISSING", "account eligibility changed")
    if suite == "strict-pointer":
        return ("HTTP_POINTER_STRICT_A_STATE_LEAK",)
    if suite == "audit-provenance":
        return ("SHADOW_AUDIT_PROVENANCE_MISSING",)
    if suite == "audit-persistence":
        return ("SHADOW_AUDIT_BODY_RETAINED",)
    if suite == "audit-async":
        return ("RECOVERED_ASYNC_ADMISSION_BLOCKED",) if name.endswith("/admission") else ("RECOVERED_ASYNC_PENALTY_MISSING",)
    if name.endswith("/first_publication"):
        return ("SHADOW_PUBLICATION_RACE_REFUSED",)
    if name.endswith("/reused_exposure"):
        return ("SHADOW_REUSED_EXPOSURE_LEAK",)
    if name.endswith("/delayed_publisher"):
        return ("SHADOW_DELAYED_PUBLICATION_REVIVED",)
    return ("SHADOW_INVALIDATED_ASSOCIATION_DISPATCH",)


def validate(phase, code, raw, suite):
    baseline = phase == "BASELINE" and suite == "strict-pointer"
    if (phase not in ("RED", "GREEN") and not baseline) or suite not in SUITES or (phase == "RED" and suite not in RED_SUITES):
        raise ValueError("invalid WS evidence phase/suite")
    COMMON.check_clean(raw)
    root, names = SUITES[suite]
    package = COMMON.PACKAGE.replace("/handler", "/securityaudit") if suite in {"audit-persistence", "audit-async"} else COMMON.PACKAGE
    leaves = {f"{root}/{name}" for name in names}
    events = []
    for line in raw.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(event, dict):
            raise ValueError("invalid event")
        events.append(event)
    counts = Counter((e.get("Action"), e.get("Test")) for e in events if e.get("Package") == package)
    failed = leaves if phase == "RED" else {name for name in leaves if counts["fail", name]} if baseline else set()
    terminal = "fail" if failed else "pass"
    if code != (1 if failed else 0):
        raise ValueError("unexpected exit")
    for name in leaves | {root}:
        if counts["run", name] != 1:
            raise ValueError(f"missing named run {name}")
    for name in leaves | {root, None}:
        expected = ("fail" if name in failed else "pass") if name in leaves else terminal
        if counts[expected, name] != 1 or counts["pass", name] + counts["fail", name] != 1:
            raise ValueError(f"incorrect terminal {name}")
    for event in events:
        if event.get("Action") == "fail" and (phase == "GREEN" or event.get("Package") != package or event.get("Test") not in leaves | {root, None}):
            raise ValueError("unrelated failure")
        if baseline and event.get("Action") == "run" and (event.get("Package") != package or event.get("Test") not in leaves | {root}):
            raise ValueError("unrelated baseline run")
    if phase == "RED" or baseline:
        for name in failed:
            output = "".join(e.get("Output", "") for e in events if e.get("Package") == package and e.get("Test") == name)
            if any(marker not in output for marker in diagnostic(suite, name)):
                raise ValueError(f"missing own unsent-turn refusal diagnostic {name}")


if __name__ == "__main__":
    phase, code, filename, suite = sys.argv[1:]
    validate(phase, int(code), Path(filename).read_text(), suite)
    print(f"{phase}: {len(SUITES[suite][1])} named {suite} scenarios verified")
