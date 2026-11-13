"""Exact named network evidence for the future-shadow TDD increment."""

import importlib.util
import json
import sys
from collections import Counter
from pathlib import Path


SPEC = importlib.util.spec_from_file_location("shadow_evidence_common", Path(__file__).with_name("assert-readable-continuation.py"))
COMMON = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(COMMON)
ROOT = "TestOpenAI409FutureShadowHTTP"
BOUNDARIES = "TestOpenAI409FutureShadowBoundaries"
LEAVES = {f"{ROOT}/stream={stream}" for stream in ("false", "true")}
BOUNDARY_NAMES = ("other_user", "other_key", "other_group", "other_model", "other_session", "other_task", "expired", "write_failed", "pending_owner", "failed_owner", "conflicted_owner", "off", "fresh_intent")
BOUNDARY_LEAVES = {f"{BOUNDARIES}/stream={stream}/{case}" for stream in ("false", "true") for case in BOUNDARY_NAMES}


def validate(phase, code, raw, boundaries=False):
    if phase not in ("RED", "GREEN") or (boundaries and phase != "GREEN"):
        raise ValueError("invalid shadow evidence phase")
    COMMON.check_clean(raw)
    events = []
    for line in raw.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(event, dict):
            raise ValueError("invalid event")
        events.append(event)
    root, leaves = (BOUNDARIES, BOUNDARY_LEAVES) if boundaries else (ROOT, LEAVES)
    terminal = "fail" if phase == "RED" else "pass"
    if code != (1 if phase == "RED" else 0):
        raise ValueError("unexpected exit status")
    actions = Counter((event.get("Action"), event.get("Test")) for event in events if event.get("Package") == COMMON.PACKAGE)
    for name in leaves | {root}:
        if actions["run", name] != 1:
            raise ValueError(f"missing or duplicate named run: {name}")
    for name in leaves | {root, None}:
        if actions[terminal, name] != 1 or actions["pass", name] + actions["fail", name] != 1:
            raise ValueError(f"incorrect terminal event: {name}")
    for event in events:
        if event.get("Action") == "fail" and (phase == "GREEN" or event.get("Package") != COMMON.PACKAGE or event.get("Test") not in leaves | {root, None}):
            raise ValueError("unrelated failure")
    if phase == "RED":
        for name in leaves:
            output = "".join(event.get("Output", "") for event in events if event.get("Package") == COMMON.PACKAGE and event.get("Test") == name)
            if "FUTURE_SHADOW_B_DISPATCH_MISSING" not in output or "encrypted_agent_recovery_readable_intent_missing" not in output:
                raise ValueError(f"missing named shadow business assertion: {name}")


def main():
    phase, code, filename, *options = sys.argv[1:]
    if options not in ([], ["--boundaries"]):
        raise ValueError("invalid option")
    validate(phase, int(code), Path(filename).read_text(), bool(options))
    print(f"{phase}: {len(BOUNDARY_LEAVES if options else LEAVES)} named shadow scenarios verified")


if __name__ == "__main__":
    try:
        main()
    except ValueError as error:
        raise SystemExit(str(error)) from error
