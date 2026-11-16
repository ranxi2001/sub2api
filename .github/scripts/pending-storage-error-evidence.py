"""Require the exact pending/storage behavioral failures, never build-only RED."""

import importlib.util
import json
import sys
from collections import Counter
from pathlib import Path

SPEC = importlib.util.spec_from_file_location(
    "clean", Path(__file__).with_name("assert-readable-continuation.py")
)
CLEAN = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CLEAN)

ROOT = "TestOpenAIAgentPendingStorageError"
CASES = (
    "own_deadline", "own_cancel", "wrapped_own_deadline", "wrapped_own_cancel",
    "backend_error_at_deadline", "backend_error_at_cancel",
    "backend_deadline_at_cancel", "backend_cancel_at_deadline",
    "backend_error_while_live", "backend_deadline_while_live",
)
RED_CASES = {
    "backend_error_at_deadline", "backend_error_at_cancel",
    "backend_deadline_at_cancel", "backend_cancel_at_deadline",
}
PACKAGE = "github.com/Wei-Shaw/sub2api/internal/service"


def validate(phase, status, transcript):
    if phase not in {"RED", "GREEN"}:
        raise ValueError("unknown phase")
    CLEAN.check_clean(transcript)
    tests = {ROOT} | {f"{ROOT}/{case}" for case in CASES}
    failed = {f"{ROOT}/{case}" for case in RED_CASES} if phase == "RED" else set()
    failures = failed | {ROOT, None} if failed else set()
    if status != (1 if failures else 0):
        raise ValueError("wrong process exit status")
    rows = []
    for line in transcript.splitlines():
        if line.startswith("go: downloading "):
            continue
        row = json.loads(line)
        if not isinstance(row, dict) or row.get("Package") != PACKAGE:
            raise ValueError("unexpected package or malformed event")
        if row.get("Action") not in {"start", "run", "output", "pass", "fail"}:
            raise ValueError("unexpected action, including skip/build failure")
        if row.get("Test") not in tests | {None}:
            raise ValueError("unrelated test")
        rows.append(row)
    counts = Counter((r.get("Action"), r.get("Test")) for r in rows)
    if counts["start", None] != 1:
        raise ValueError("missing/repeated package start")
    for name in tests | {None}:
        if name is not None and counts["run", name] != 1:
            raise ValueError(f"missing/repeated test run: {name}")
        expected = "fail" if name in failures else "pass"
        if counts[expected, name] != 1 or counts["pass", name] + counts["fail", name] != 1:
            raise ValueError(f"wrong/missing/repeated outcome: {name}")
    for name in tests - {ROOT}:
        output = "".join(r.get("Output", "") for r in rows if r.get("Test") == name)
        required = ("PENDING_STORAGE_ERROR_MASKED:", "store_unavailable", "delivery_pending") if name in failed else (
            "PENDING_STORAGE_ERROR_CONTROL_OK:", "found=false", "no ownership publication",
        )
        if any(marker not in output for marker in required):
            raise ValueError(f"missing behavioral evidence: {name}")
    return {"phase": phase, "exit_status": status, "tests": len(tests), "behavioral_red_leaves": len(failed)}


if __name__ == "__main__":
    phase, status, path = sys.argv[1:]
    print(json.dumps(validate(phase, int(status), Path(path).read_text()), sort_keys=True))
