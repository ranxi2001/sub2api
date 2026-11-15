"""Require named network behavior; compiler/setup failures are never RED."""

import importlib.util
import json
import sys
from collections import Counter
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("readable_common", Path(__file__).with_name("assert-readable-continuation.py"))
COMMON = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(COMMON)
PACKAGE = "github.com/Wei-Shaw/sub2api/internal/handler"
ROOT = "TestOpenAIWSMixedOwnerContinuation"
MODES = ("http_bridge", "ctx_pool", "shared", "dedicated", "passthrough")
BOUNDARIES = {f"{state}_{order}" for state in ("pending", "failed", "conflicted") for order in ("first", "last")} | {
    "unknown", "unpaired_call", "unpaired_output", "duplicate_call", "duplicate_output", "incomplete", "off", "no_intent",
}
SUITES = {
    "red": {f"{ROOT}/turn_2"},
    "continuation": {f"{ROOT}/turn_{turn}" for turn in (2, 53)},
    "boundaries": {f"TestOpenAIWSMixedOwnerBoundaries/{name}" for name in BOUNDARIES},
    "transports": {f"TestOpenAIWSMixedOwnerTransports/{mode}/{case}" for mode in MODES for case in ("live", "fresh", "binding_changed")},
}


def validate(suite, status, raw):
    COMMON.check_clean(raw)
    leaves = SUITES[suite]
    expected = "fail" if suite == "red" else "pass"
    if status != (1 if suite == "red" else 0):
        raise ValueError("unexpected process exit")
    rows = [json.loads(line) for line in raw.splitlines() if line.startswith("{")]
    ancestors = {leaf.split("/")[0] for leaf in leaves} # t.Run uses flattened mode/case names.
    tests = leaves | ancestors
    counts = Counter((r.get("Action"), r.get("Test")) for r in rows if r.get("Package") == PACKAGE)
    for name in tests:
        if counts["run", name] != 1 or counts[expected, name] != 1 or counts["pass", name] + counts["fail", name] != 1:
            raise ValueError(f"missing/duplicate/wrong named result: {name}")
    if counts[expected, None] != 1 or counts["pass", None] + counts["fail", None] != 1:
        raise ValueError("missing package completion")
    for row in rows:
        if row.get("Action") in ("run", "pass", "fail") and (row.get("Package") != PACKAGE or row.get("Test") not in tests | {None}):
            raise ValueError("unrelated execution cannot satisfy evidence")
    if suite == "red":
        own = "".join(r.get("Output", "") for r in rows if r.get("Package") == PACKAGE and r.get("Test") in leaves)
        for marker in ("MIXED_COMPLETED_OWNER_NOT_RESUMED", "encrypted_agent_recovery_owner_conflict"):
            if marker not in own:
                raise ValueError("missing own actual handler refusal")


if __name__ == "__main__":
    suite, status, filename = sys.argv[1:]
    validate(suite, int(status), Path(filename).read_text())
    print(f"Verified {suite}: {len(SUITES[suite])} named handler scenarios")
