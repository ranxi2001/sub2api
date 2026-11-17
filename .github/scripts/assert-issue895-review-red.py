"""Require the exact F1 RED failures and every intact/fence/400 control."""

import json
import re
import sys
from collections import Counter
from pathlib import Path


PACKAGE = "github.com/Wei-Shaw/sub2api/internal/service"
PROJECTION = "TestIssue892OpaqueEnvironmentProjectionRefused"
ACTUAL = "TestIssue892ActualSendOpaqueIntegrityAndDelivery"
FENCES = "TestIssue892PreparedUnknownOwnerRechecksFences"
RECOVERY = "TestIssue892ManualTimezone400Recovery"
MARKERS = {
    **{f"{PROJECTION}/{kind}/{mode}": "Issue895 F1 projection must refuse a changed opaque envelope"
       for kind in ("oauth", "apikey") for mode in ("http", "ws")},
    **{f"{ACTUAL}/{mode}/{mutation}": "Issue895 F1 actual send must reject a changed opaque item"
       for mode in ("http", "ws") for mutation in ("envelope", "drop")},
}
CONTROLS = (
    {f"{ACTUAL}/{mode}/intact" for mode in ("http", "ws")}
    | {f"{FENCES}/{mode}/{fence}" for mode in ("http", "ws") for fence in ("pending", "failed")}
    | {f"{RECOVERY}/{mode}" for mode in ("enabled", "disabled", "ordinary400", "SSE")}
)


def expected_results():
    # Each fixture calls t.Run once with a slash-containing name. Those slash
    # prefixes are filters, not separately created intermediate test events.
    return {
        **dict.fromkeys(CONTROLS, "pass"),
        **dict.fromkeys(MARKERS, "fail"),
        PROJECTION: "fail",
        ACTUAL: "fail",
        FENCES: "pass",
        RECOVERY: "pass",
        None: "fail",
    }


def validate(code, raw):
    if code != 1:
        raise ValueError("expected ordinary go test failure exit 1")
    if re.search(r"build failed|build-fail|undefined:|setup failed|no tests to run|"
                 r"panic:|panic_recovered|fatal error:|runtime error:|WARNING: DATA RACE|"
                 r"signal:|timed out|timeout|deadline exceeded|--- SKIP:", raw, re.I):
        raise ValueError("invalid build/setup/runtime evidence")
    events = []
    for line in raw.splitlines():
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError as error:
            raise ValueError("non-JSON setup or process output") from error
        if not isinstance(event, dict) or event.get("Package") != PACKAGE:
            raise ValueError("unexpected package or malformed event")
        events.append(event)
    expected = expected_results()
    counts = Counter()
    outputs = {}
    for event in events:
        action, name = event.get("Action"), event.get("Test")
        if action not in ("start", "run", "pause", "cont", "output", "pass", "fail"):
            raise ValueError("unexpected action, skip or build event")
        if name not in expected or (action == "start" and name is not None):
            raise ValueError("unselected test or package start")
        counts[action, name] += 1
        if action in ("pass", "fail") and action != expected[name]:
            raise ValueError(f"unexpected result: {name}")
        if action == "output":
            output = event.get("Output")
            if not isinstance(output, str):
                raise ValueError("malformed output")
            outputs[name] = outputs.get(name, "") + output
    if counts["start", None] != 1:
        raise ValueError("missing or duplicate package start")
    for name, result in expected.items():
        if name is not None and counts["run", name] != 1:
            raise ValueError(f"missing or duplicate run: {name}")
        if counts[result, name] != 1 or counts["pass", name] + counts["fail", name] != 1:
            raise ValueError(f"missing or duplicate terminal: {name}")
    for name, marker in MARKERS.items():
        if marker not in outputs.get(name, ""):
            raise ValueError(f"wrong business assertion: {name}")


if __name__ == "__main__":
    try:
        validate(int(sys.argv[1]), Path(sys.argv[2]).read_text())
    except (ValueError, OSError, IndexError) as error:
        raise SystemExit(str(error)) from error
    print("F1 RED: exactly 8 named failures and 10 passing controls verified")
