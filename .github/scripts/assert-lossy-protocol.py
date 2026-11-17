"""Require named tool-loop evidence; build errors, skipped or empty runs fail."""
import importlib.util
import json
import sys
from collections import Counter
from pathlib import Path

spec = importlib.util.spec_from_file_location("common", Path(__file__).with_name("assert-readable-continuation.py"))
common = importlib.util.module_from_spec(spec)
spec.loader.exec_module(common)
HTTP = "TestOpenAILossyProtocolBetaToolLoop"
WS = "TestOpenAILossyProtocolBetaWSBridgeToolLoop"
PROJECTION = "TestOpenAILossyProtocolCompletedDiscoveryProjection"


def validate(phase, status, raw, suite):
    common.check_clean(raw)
    rows = [json.loads(line) for line in raw.splitlines() if line.startswith("{")]
    package = "github.com/Wei-Shaw/sub2api/internal/" + ("service" if suite == "projection" else "handler")
    rows = [row for row in rows if row.get("Package") == package]
    counts = Counter((row.get("Action"), row.get("Test")) for row in rows)
    if suite == "projection":
        leaves = {PROJECTION}
    elif suite == "loops":
        leaves = {f"{HTTP}/lossy={lossy}/{account}/stream={stream}"
                  for lossy in ("false", "true") for account in ("apikey", "oauth") for stream in ("false", "true")}
        leaves |= {f"{WS}/lossy={lossy}/{account}"
                   for lossy in ("false", "true") for account in ("apikey", "oauth")}
    else:
        raise ValueError("unknown suite")
    expected_failed = set()
    for name in sorted(leaves):
        if counts["run", name] != 1 or counts["pass", name] + counts["fail", name] != 1:
            raise ValueError(f"missing/repeated named execution: {name}")
        red = phase == "RED" and (suite == "projection" or name.startswith(WS) or "/lossy=true/" in name)
        outcome = "fail" if red else "pass"
        if counts[outcome, name] != 1:
            raise ValueError(f"expected {outcome}: {name}")
        if red:
            expected_failed.add(name)
            output = "".join(row.get("Output", "") for row in rows if row.get("Test") == name)
            markers = ("LOSSY_DISCOVERY_PROJECTION_REFUSED",) if suite == "projection" else (
                "LOSSY_TOOL_PROTOCOL_LOST", "LOSSY_DISCOVERY_NEXT_TURN_REFUSED")
            if not any(marker in output for marker in markers):
                raise ValueError(f"missing tool behavior assertion: {name}")
        print(f"{phase} {outcome}: {name}")
    expected_status = 1 if expected_failed else 0
    if status != expected_status or counts["fail" if expected_failed else "pass", None] != 1:
        raise ValueError("unexpected exit/package terminal")
    for row in rows:
        if row.get("Action") != "fail" or row.get("Test") is None:
            continue
        name = row["Test"]
        if not any(leaf == name or leaf.startswith(name + "/") for leaf in expected_failed):
            raise ValueError(f"unrelated failure: {name}")


if __name__ == "__main__":
    phase, status, path, suite = sys.argv[1:]
    if phase not in {"RED", "GREEN"}:
        raise ValueError("unknown phase")
    validate(phase, int(status), Path(path).read_text(), suite)
