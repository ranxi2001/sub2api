"""Require all eight real config-switch cases; reject setup failures as RED."""

import json
import sys
from collections import Counter
from pathlib import Path

PACKAGE = "github.com/Wei-Shaw/sub2api/internal/service"
ROOTS = {"TestExcelBPSRequestProxyConfigSwitch" + path for path in ("Forward", "Images", "SIWC", "OAuth")}
LEAVES = {root + "/" + mode for root in ROOTS for mode in ("single_B", "single_empty")}
NODES = ROOTS | LEAVES
ESCAPED = "CONFIG_SWITCH_REFRESH_EGRESS_ESCAPED"
REACHED = ("CONFIG_SWITCH_ROUTE_READY", "CONFIG_SWITCH_SINGLE_SAVED")


def validate(phase, code, text):
    if phase not in {"RED", "GREEN"} or code != (1 if phase == "RED" else 0):
        raise ValueError("wrong phase or Go exit status")
    try:
        events = [json.loads(line) for line in text.splitlines() if line.strip()]
    except (ValueError, TypeError) as exc:
        raise ValueError("non-JSON build/runtime output") from exc
    if not events or any(not isinstance(event, dict) for event in events):
        raise ValueError("missing JSON events")
    if any(event.get("Package") != PACKAGE for event in events):
        raise ValueError("wrong package")
    output = "".join(event.get("Output", "") for event in events)
    poison = ("[no tests to run]", "DATA RACE", "panic:", "fatal error:", "signal: killed", "undefined:", "build failed", "no required module provides package", "--- SKIP:")
    if any(marker in output for marker in poison) or any(event.get("Action") in {"skip", "build-fail", "build-output"} or event.get("FailedBuild") for event in events):
        raise ValueError("build/runtime/skip contamination")
    named = [event for event in events if event.get("Test")]
    if any(event["Test"] not in NODES for event in named):
        raise ValueError("unexpected test")
    runs = Counter(event["Test"] for event in named if event.get("Action") == "run")
    terminal = "fail" if phase == "RED" else "pass"
    ends = Counter(event["Test"] for event in named if event.get("Action") in {"pass", "fail", "skip"})
    if runs != Counter({name: 1 for name in NODES}) or ends != runs:
        raise ValueError("missing or repeated test nodes")
    if any(event.get("Action") != terminal for event in named if event.get("Action") in {"pass", "fail", "skip"}):
        raise ValueError("wrong test verdict")
    packages = [event["Action"] for event in events if not event.get("Test") and event.get("Action") in {"pass", "fail", "skip"}]
    if packages != [terminal]:
        raise ValueError("wrong package verdict")
    for leaf in LEAVES:
        own = "".join(event.get("Output", "") for event in named if event["Test"] == leaf)
        if not all(marker in own for marker in REACHED):
            raise ValueError("route barrier or real admin save not reached: " + leaf)
        if (ESCAPED in own) != (phase == "RED"):
            raise ValueError("wrong business failure: " + leaf)
    return {"phase": phase, "go_exit": code, "roots": len(ROOTS), "leaves": len(LEAVES), "nodes": len(NODES)}


if __name__ == "__main__":
    print(json.dumps(validate(sys.argv[1], int(sys.argv[2]), Path(sys.argv[3]).read_text()), sort_keys=True))
