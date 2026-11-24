"""Validate named Forward behavior; setup errors cannot count as numeric RED."""

import json
import re
import sys
from collections import Counter
from pathlib import Path

PACKAGE = "github.com/Wei-Shaw/sub2api/internal/service"
ROOT = "TestClaudeAPIKeySignatureRetryPreservesNumbers"
DEFAULTS = "TestClaudeAPIKeySignatureRetryDefaults"
MARKERS = {
    f"{ROOT}/stage_one": "stage_one_tool_input_record_id_exact",
    f"{ROOT}/stage_two": "stage_two_tool_input_record_id_exact",
}
CONTROLS = {
    f"{DEFAULTS}/{name}"
    for name in (
        "unconfigured",
        "explicitly_disabled",
        "disabled_globally",
        "saved_before_API_key_setting_existed",
        "explicit_passthrough",
    )
}


def validate(phase, status, raw, stderr):
    if phase not in {"red", "green"}:
        raise ValueError("unknown oracle phase")
    rows = [json.loads(line) for line in raw.splitlines() if line.strip()]
    output = "".join(row.get("Output", "") for row in rows) + stderr
    if re.search(
        r"build failed|setup failed|no tests to run|panic[: ]|fatal error:|"
        r"runtime error:|SIGSEGV|test timed out|WARNING: DATA RACE|\[no test files\]",
        output,
        re.IGNORECASE,
    ) or any(row.get("Action") == "skip" for row in rows):
        raise ValueError("setup, panic, race, timeout or skip contamination")
    red = phase == "red"
    if status != (1 if red else 0):
        raise ValueError(f"unexpected process exit: {status}")
    expected = {ROOT, *MARKERS}
    if not red:
        expected |= {DEFAULTS, *CONTROLS}
    counts = Counter((row.get("Action"), row.get("Test")) for row in rows if row.get("Package") == PACKAGE)
    outcome = "fail" if red else "pass"
    for name in expected:
        if counts["run", name] != 1 or counts[outcome, name] != 1 or counts["pass", name] + counts["fail", name] != 1:
            raise ValueError(f"missing, duplicate or wrong named result: {name}")
    if counts[outcome, None] != 1 or counts["pass", None] + counts["fail", None] != 1:
        raise ValueError("missing or wrong package completion")
    for row in rows:
        if row.get("Action") in {"run", "pass", "fail"}:
            if row.get("Package") != PACKAGE or row.get("Test") not in expected | {None}:
                raise ValueError("unrelated execution cannot satisfy evidence")
    if red:
        for name, marker in MARKERS.items():
            own = "".join(row.get("Output", "") for row in rows if row.get("Package") == PACKAGE and row.get("Test") == name)
            for required in (marker, "Not equal", "9007199254740993", "9007199254740992"):
                if required not in own:
                    raise ValueError(f"missing numeric behavioral assertion {required}: {name}")


if __name__ == "__main__":
    phase, exitcode, jsonl, stderr = sys.argv[1:]
    validate(phase, int(Path(exitcode).read_text()), Path(jsonl).read_text(), Path(stderr).read_text())
    print(f"Verified {phase}: both named Forward numeric stages" + (" and five settings/transport controls" if phase == "green" else " failed at the expected integer assertion"))
