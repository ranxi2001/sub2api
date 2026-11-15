"""Accept named business failures only; shared validator rejects dirty logs."""
import importlib.util
import json
import sys
from collections import Counter
from pathlib import Path

spec = importlib.util.spec_from_file_location("common", Path(__file__).with_name("assert-readable-continuation.py"))
common = importlib.util.module_from_spec(spec)
spec.loader.exec_module(common)
ROOT = "TestIssue854OwnerContinuation"
MODES = ("http_bridge", "ctx_pool", "shared", "dedicated", "passthrough")
LEAVES = {f"{ROOT}/{mode}/{case}" for mode in MODES for case in ("original", "fresh", "guard_live")}
PARENTS = {ROOT}


def inventory(suite):
    if suite == "lifecycle":
        return LEAVES, PARENTS, {n for n in LEAVES if n.endswith("/guard_live")}, ("WS_READABLE_B_DISPATCH_MISSING", "The proven encrypted agent owner is currently unavailable")
    if suite == "persistence":
        root = "TestIssue854OwnerFailurePersistence"
        leaves = {f"{root}/{mode}/{connection}" for mode in MODES for connection in ("guard", "fresh")}
        return leaves, {root}, leaves, ("ISSUE854_FAILURE_NOT_PERSISTED",)
    if suite == "r1":
        root = "TestIssue854FreshOwnerSelection"
        leaves = {f"{root}/{mode}/{case}" for mode in MODES for case in ("disabled", "cooldown", "off", "pending", "failed", "conflict", "unfinished")}
        return leaves, {root}, {n for n in leaves if n.endswith(("/disabled", "/cooldown"))}, ("ISSUE854_R1_INITIAL_OWNER_REFUSED", "encrypted_agent_recovery_owner_")
    if suite == "http-empty-image":
        root = "TestIssue854HTTPTransformMatrix"
        leaves = {f"{root}/{account}/stream={stream}/empty_image" for account in ("apikey", "oauth") for stream in ("false", "true")}
        return leaves, {root}, leaves, ("HTTP_REBUILD_B_DISPATCH_MISSING", "encrypted_agent_recovery_wire_changed")
    if suite == "http-transform":
        root = "TestIssue854HTTPTransformMatrix"
        cases = ("metadata", "message_namespace", "tool_namespace", "empty_image", "reasoning_summary", "compaction_order", "timezone_manual", "timezone_auto", "lite", "reserved_alias", "namespace_declaration", "namespace_flatten", "schema_null", "new_task")
        leaves = {f"{root}/{account}/stream={stream}/{case}" for account in ("apikey", "oauth") for stream in ("false", "true") for case in cases}
        return leaves, {root}, set(), ()
    if suite == "http-route":
        root = "TestIssue854HTTPRouteAdmission"
        leaves = {f"{root}/stream={stream}/native_available={available}" for stream in ("false", "true") for available in ("false", "true")}
        return leaves, {root}, set(), ()
    if suite == "postgres":
        root = "TestIssue854PostgresRoundtrip"
        return {f"{root}/{name}" for name in ("ws_guard", "ws_fresh", "sse")}, {root}, set(), ()
    if suite == "ws-upstream":
        root = "TestIssue854WSOwnerRateLimit"
        leaves = {f"{root}/{mode}/encrypted_agent_recovery_{code}" for mode in MODES for code in ("owner_rate_limited", "untrusted_fixture")}
        return leaves, {root}, {n for n in leaves if n.endswith("owner_rate_limited")}, ("ISSUE854_WS_OWNER_LIMIT_CODE_LOST",)
    if suite == "upstream":
        root = "TestIssue854UpstreamOwnerRateLimit"
        parents = {root}
        leaves = {f"{root}/stream={stream}/encrypted_agent_recovery_{code}" for stream in ("false", "true") for code in ("owner_rate_limited", "untrusted_fixture")}
        return leaves, parents, {n for n in leaves if n.endswith("owner_rate_limited")}, ("ISSUE854_OWNER_LIMIT_CODE_LOST",)
    raise ValueError("invalid suite")


def validate(phase, status, raw, suite="lifecycle"):
    leaves, parents, red_failures, markers = inventory(suite)
    if phase not in ("RED", "GREEN"):
        raise ValueError("invalid phase")
    if phase == "RED" and suite in ("http-transform", "http-route"):
        raise ValueError("updated policy matrix is a GREEN gate; use the named empty-image RED and archived original matrix")
    common.check_clean(raw)
    if "requires real redis-server in CI" in raw:
        raise ValueError("fixture setup failure")
    events = []
    for line in raw.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(event, dict):
            raise ValueError("invalid event")
        events.append(event)
    counts = Counter((e.get("Action"), e.get("Test")) for e in events if e.get("Package") == common.PACKAGE)
    failures = red_failures if phase == "RED" else set()
    failed_parents = {p for p in parents if any(n.startswith(p + "/") for n in failures)}
    expected = {n: "fail" if n in failures | failed_parents else "pass" for n in leaves | parents}
    expected[None] = "fail" if failures else "pass"
    if status != (1 if failures else 0):
        raise ValueError("wrong exit status")
    for name, terminal in expected.items():
        if name and counts["run", name] != 1:
            raise ValueError(f"missing/repeated run: {name}")
        if counts[terminal, name] != 1 or counts["pass", name] + counts["fail", name] != 1:
            raise ValueError(f"wrong terminal: {name}")
    for e in events:
        if e.get("Action") in ("run", "pass", "fail") and (e.get("Package") != common.PACKAGE or e.get("Test") not in expected):
            raise ValueError("unrelated test")
    for name in failures:
        output = "".join(e.get("Output", "") for e in events if e.get("Test") == name)
        for marker in markers:
            if marker not in output:
                raise ValueError(f"missing business assertion: {name}: {marker}")


if __name__ == "__main__":
    phase, status, filename, *extra = sys.argv[1:]
    suite = extra[0] if extra else "lifecycle"
    validate(phase, int(status), Path(filename).read_text(), suite)
    print(f"{phase}: all {len(inventory(suite)[0])} named {suite} leaves verified")
