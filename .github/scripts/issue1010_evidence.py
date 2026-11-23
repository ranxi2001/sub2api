"""Validate executed, per-leaf Go evidence for the Issue1010 native oracle."""

import json
from pathlib import Path
import sys


PACKAGE = "github.com/Wei-Shaw/sub2api/internal/integration/accountconnectiontiming"
ROOT = "TestGatewayAccountConnectionNativeHTTP"
KINDS = ("new_api", "sub2api")
PROTOCOLS = ("responses", "chat", "anthropic", "gemini")
LEAVES = {f"{ROOT}/{kind}/{protocol}" for kind in KINDS for protocol in PROTOCOLS}
# Each slash-delimited kind/protocol is one t.Run name, not two nested tests.
NODES = LEAVES | {ROOT}
ASSERTION = "gateway probe must reach synthetic upstream exactly once"
GUARD = "GATEWAY_PROTOCOLS_PENDING"
CONTAMINATION = (
    "[build failed]", "build-fail", "setup failed", "no tests to run",
    "panic:", "fatal error:", "warning: data race", "race detected during execution",
    "segmentation fault", "sigsegv", "sigabrt", "sigbus", "test timed out",
    "signal: killed", "runtime: out of memory",
)


def validate(raw, mode):
    if mode not in ("red", "green"):
        raise ValueError(f"unknown evidence mode: {mode}")
    if any(marker in raw.lower() for marker in CONTAMINATION):
        raise ValueError("build/setup/crash/race/empty-test contamination")
    events = [json.loads(line) for line in raw.splitlines() if line.startswith("{")]
    if not events:
        raise ValueError("missing executed Go test events")
    terminal = "fail" if mode == "red" else "pass"
    runs = {}
    results = {}
    output = {}
    package_results = []
    for event in events:
        action = event.get("Action")
        if action == "skip":
            raise ValueError("skipped oracle")
        if event.get("Package") != PACKAGE:
            raise ValueError("unexpected package or malformed Go event")
        name = event.get("Test")
        if name:
            if name not in NODES:
                raise ValueError(f"unexpected oracle test: {name}")
            if action == "run":
                runs[name] = runs.get(name, 0) + 1
            elif action in ("fail", "pass"):
                results.setdefault(name, []).append(action)
            elif action == "output":
                output[name] = output.get(name, "") + event.get("Output", "")
        elif action in ("fail", "pass"):
            package_results.append(action)
    if runs != dict.fromkeys(NODES, 1):
        raise ValueError("missing or duplicate executed oracle tests")
    if results != {name: [terminal] for name in NODES}:
        raise ValueError("missing, duplicate or unexpected test outcomes")
    if package_results != [terminal]:
        raise ValueError("missing or unexpected package outcome")
    if mode == "red":
        for name in sorted(LEAVES):
            leaf_output = output.get(name, "")
            if ASSERTION not in leaf_output or GUARD not in leaf_output:
                raise ValueError(f"{name}: missing its own business assertion or historical guard")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit("usage: issue1010_evidence.py red|green oracle.jsonl")
    validate(Path(sys.argv[2]).read_text(), sys.argv[1])
