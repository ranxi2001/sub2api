"""Named HTTP task-rebuild TDD evidence; reuse the reviewed pollution gate."""

import importlib.util
import json
import sys
from collections import Counter
from pathlib import Path


SPEC = importlib.util.spec_from_file_location(
    "readable_evidence", Path(__file__).with_name("assert-readable-continuation.py")
)
COMMON = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(COMMON)
ROOT = "TestOpenAI409HTTPTaskRebuild"
BOUNDARIES = "TestOpenAI409HTTPRebuildBoundaries"
PACKAGE = COMMON.PACKAGE
LEAVES = {
    f"{ROOT}/{account}/stream={stream}/{pointer}/{outcome}"
    for account in ("apikey", "oauth")
    for stream in ("false", "true")
    for pointer in ("intent", "previous", "conversation")
    for outcome in ("completed", "rejected", "write_failed")
}
BOUNDARY_LEAVES = {
    f"{BOUNDARIES}/{account}/stream={stream}/{boundary}"
    for account in ("apikey", "oauth")
    for stream in ("false", "true")
    for boundary in ("pointer_only", "opaque_only", "delivery_pending", "delivery_failed", "b_disabled")
}
DIAGNOSTIC = "HTTP_REBUILD_B_DISPATCH_MISSING"
OLD_REFUSALS = (
    "encrypted_agent_recovery_owner_unavailable",
    "encrypted_agent_recovery_http_previous_response_unsupported",
    "encrypted_agent_recovery_wire_changed",
)


def validate(phase, exit_code, transcript, boundaries=False):
    if phase not in {"RED", "GREEN"} or (boundaries and phase != "GREEN"):
        raise ValueError("invalid task-rebuild phase")
    COMMON.check_clean(transcript)
    events = []
    for line in transcript.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(event, dict):
            raise ValueError("invalid go-test event")
        events.append(event)
    root, leaves = (BOUNDARIES, BOUNDARY_LEAVES) if boundaries else (ROOT, LEAVES)
    terminal = "fail" if phase == "RED" else "pass"
    expected_exit = 1 if phase == "RED" else 0
    if exit_code != expected_exit:
        raise ValueError("unexpected go-test exit status")
    actions = Counter(
        (event.get("Action"), event.get("Test"))
        for event in events if event.get("Package") == PACKAGE
    )
    for name in leaves | {root}:
        if actions["run", name] != 1:
            raise ValueError(f"missing/repeated named test: {name}")
    for name in leaves | {root, None}:
        if actions[terminal, name] != 1 or actions["pass", name] + actions["fail", name] != 1:
            raise ValueError(f"missing or incorrect test/package terminal: {name}")
    for event in events:
        if event.get("Action") == "fail" and (
            phase == "GREEN" or event.get("Package") != PACKAGE or event.get("Test") not in leaves | {root, None}
        ):
            raise ValueError("unexpected failure outside named task-rebuild RED")
    if phase == "RED":
        for name in leaves:
            output = "".join(
                event.get("Output", "") for event in events
                if event.get("Package") == PACKAGE and event.get("Test") == name
            )
            if DIAGNOSTIC not in output or not any(code in output for code in OLD_REFUSALS):
                raise ValueError(f"missing named B-dispatch refusal assertion: {name}")


def main():
    phase, code, filename, *options = sys.argv[1:]
    if options not in ([], ["--boundaries"]):
        raise ValueError("unknown option")
    boundaries = bool(options)
    validate(phase, int(code), Path(filename).read_text(), boundaries)
    print(f"{phase}: {len(BOUNDARY_LEAVES if boundaries else LEAVES)} named task-rebuild scenarios verified")


if __name__ == "__main__":
    try:
        main()
    except ValueError as exc:
        raise SystemExit(str(exc)) from exc
