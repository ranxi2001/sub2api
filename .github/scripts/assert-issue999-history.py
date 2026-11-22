"""CI evidence for Issue999: require named behavioral RED and clean candidate GREEN."""

import json
import runpy
import sys
from collections import Counter
from pathlib import Path

HANDLER = "github.com/Wei-Shaw/sub2api/internal/handler"
SERVICE = "github.com/Wei-Shaw/sub2api/internal/service"
HISTORY = "TestIssue999HTTPBridgeAssistantHistory"
POINTER = "TestIssue999HTTPBridgeRejectsForeignContinuation"
DELIVERY = "TestIssue999HTTPBridgeDeliveryControls"
HISTORY_LEAVES = {f"{HISTORY}/{name}" for name in ("empty_terminal", "full_terminal")}
POINTER_LEAVES = {f"{POINTER}/{name}" for name in ("wrong_pointer", "wrong_session", "omitted_identity")}
DELIVERY_LEAVES = {
    f"{DELIVERY}/{name}" for name in (
        "completed", "full_terminal_only", "legacy_implicit_completed", "failed",
        "incomplete", "completed_with_incomplete_status", "cancelled", "completed_with_error",
        "eof_before_terminal", "added_only", "unfinished_item", "failed_item", "invalid_index",
        "fractional_index", "missing_index", "undelivered_item", "undelivered_terminal",
        "item_write_error", "terminal_write_error",
    )
}


def validate(phase, exit_code, path):
    if phase not in {"RED", "GREEN"}:
        raise ValueError("unknown phase")
    transcript = Path(path).read_text()
    # Shared scanner rejects raw setup/compiler diagnostics, races, timeouts,
    # recovered panics and skips. These are never acceptable behavioral RED.
    runpy.run_path(str(Path(__file__).with_name("assert-readable-continuation.py")))["check_clean"](transcript)
    rows = []
    for line in transcript.splitlines():
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(row, dict):
            raise ValueError("invalid go test event")
        rows.append(row)
    if any(r.get("Action") in {"build-fail", "skip"} for r in rows):
        raise ValueError("build failure or skip cannot prove behavior")
    expected = {HANDLER: HISTORY_LEAVES | {HISTORY}}
    if phase == "GREEN":
        expected[HANDLER] |= POINTER_LEAVES | {POINTER}
        expected[SERVICE] = DELIVERY_LEAVES | {DELIVERY}
    actions = Counter((r.get("Package"), r.get("Action"), r.get("Test")) for r in rows)
    for package, names in expected.items():
        for name in names:
            if actions[package, "run", name] != 1 or actions[package, "pass", name] + actions[package, "fail", name] != 1:
                raise ValueError(f"missing/repeated test execution or terminal: {name}")
        terminal = "pass" if phase == "GREEN" else "fail"
        if actions[package, terminal, None] != 1 or actions[package, "pass", None] + actions[package, "fail", None] != 1:
            raise ValueError("missing or incorrect package terminal")
    if phase == "RED":
        if exit_code != 1:
            raise ValueError("old code did not fail normally")
        for name in HISTORY_LEAVES:
            output = "".join(r.get("Output", "") for r in rows if r.get("Package") == HANDLER and r.get("Test") == name)
            if actions[HANDLER, "fail", name] != 1 or "ISSUE999_ASSISTANT_HISTORY_LOST" not in output:
                raise ValueError(f"missing named assistant-history behavioral failure: {name}")
        for row in rows:
            if row.get("Action") == "fail" and (row.get("Package") != HANDLER or row.get("Test") not in expected[HANDLER] | {None}):
                raise ValueError("unrelated failure cannot prove RED")
    else:
        if exit_code != 0 or any(r.get("Action") == "fail" for r in rows):
            raise ValueError("candidate failed")
        for package, names in expected.items():
            if any(actions[package, "pass", name] != 1 for name in names):
                raise ValueError("not all candidate behavior scenarios passed")
    print(f"{phase}: named Issue999 history evidence accepted")


if __name__ == "__main__":
    try:
        validate(sys.argv[1], int(sys.argv[2]), sys.argv[3])
    except ValueError as exc:
        raise SystemExit(str(exc)) from exc
