"""Original WS audit/NextTurn assertions, with exact business RED reasons."""
import importlib.util
import json
import re
import sys
from collections import Counter
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("clean", Path(__file__).with_name("assert-readable-continuation.py"))
CLEAN = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CLEAN)
PACKAGE = "github.com/Wei-Shaw/sub2api/internal/handler"
AUDIT = "TestOpenAI409WSRecoveredAudit"
NEXT = "TestOpenAI409WSRecoveredAuditNextTurn"
MODES = ("http_bridge", "ctx_pool", "shared", "dedicated", "passthrough")
LEAVES = {f"{AUDIT}/{mode}/{policy}" for mode in MODES for policy in ("prompt", "selected")} | {f"{NEXT}/{mode}" for mode in MODES}


def validate(phase, status, raw):
    CLEAN.check_clean(raw)
    if phase not in ("RED", "GREEN") or status != (1 if phase == "RED" else 0):
        raise ValueError("unexpected phase or process status")
    rows = [json.loads(line) for line in raw.splitlines() if line.startswith("{")]
    outcome = "fail" if phase == "RED" else "pass"
    tests = LEAVES | {AUDIT, NEXT}
    counts = Counter((row.get("Action"), row.get("Test")) for row in rows if row.get("Package") == PACKAGE)
    for name in tests:
        if counts["run", name] != 1 or counts[outcome, name] != 1 or counts["pass", name] + counts["fail", name] != 1:
            raise ValueError(f"missing/duplicate/wrong test outcome: {name}")
    if counts[outcome, None] != 1 or counts["pass", None] + counts["fail", None] != 1:
        raise ValueError("missing package completion")
    for row in rows:
        if row.get("Action") in ("run", "pass", "fail") and (row.get("Package") != PACKAGE or row.get("Test") not in tests | {None}):
            raise ValueError("unrelated result is not evidence")
    if phase == "RED":
        for name in LEAVES:
            output = "".join(row.get("Output", "") for row in rows if row.get("Package") == PACKAGE and row.get("Test") == name)
            expected = "response.completed" if name.startswith(NEXT + "/") else "prompt_guard_blocked" if name.endswith("/prompt") else "content_policy_violation"
            actual = "error" if name.startswith(NEXT + "/") else "encrypted_agent_recovery_readable_intent_missing"
            if not re.search(r'expected:\s*"' + re.escape(expected) + r'"', output) or not re.search(r'actual\s*:\s*"' + re.escape(actual) + r'"', output):
                raise ValueError(f"not the original shadow/audit assertion: {name}")
            if "encrypted_agent_recovery_readable_intent_missing" not in output:
                raise ValueError(f"missing own actual business refusal: {name}")


if __name__ == "__main__":
    phase, status, path = sys.argv[1:]
    validate(phase, int(status), Path(path).read_text())
    print(f"{phase}: all 15 original WS recovered-audit/NextTurn scenarios verified")
