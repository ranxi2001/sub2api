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
DISCOVERY = "TestOpenAILossyProtocolDiscoveryProofFences"
DISCOVERY_RED = {"forged_pair", "call", "call_metadata", "result_metadata", "schema_after_completion", "metadata_after_completion", "failed_receipt", "pending_receipt"}
DISCOVERY_GREEN = DISCOVERY_RED | {"none", "model", "owner", "epoch", "final_schema", "final_metadata", "final_owner", "final_epoch", "final_proof", "accepted_owner", "accepted_epoch"}


def validate(phase, status, raw, suite):
    common.check_clean(raw)
    rows = [json.loads(line) for line in raw.splitlines() if line.startswith("{")]
    package = "github.com/Wei-Shaw/sub2api/internal/" + ("service" if suite in {"projection", "discovery"} else "handler")
    rows = [row for row in rows if row.get("Package") == package]
    counts = Counter((row.get("Action"), row.get("Test")) for row in rows)
    if suite == "projection":
        leaves = {PROJECTION}
    elif suite == "discovery":
        leaves = {f"{DISCOVERY}/{name}" for name in (DISCOVERY_RED if phase == "RED" else DISCOVERY_GREEN)}
    elif suite in {"loops", "namespace"}:
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
        if suite == "namespace":
            red = phase == "RED" and name.startswith(HTTP) and "/apikey/" in name
        else:
            # v2.21.6 also predates the API-key discovery-namespace repair.
            # Its lossy=false API-key leaves must fail that exact assertion;
            # OAuth lossy=false remains the historical passing control.
            red = phase == "RED" and (suite in {"projection", "discovery"} or name.startswith(WS) or "/lossy=true/" in name or "/apikey/" in name)
        outcome = "fail" if red else "pass"
        if counts[outcome, name] != 1:
            raise ValueError(f"expected {outcome}: {name}")
        if red:
            expected_failed.add(name)
            output = "".join(row.get("Output", "") for row in rows if row.get("Test") == name)
            markers = ("LOSSY_DISCOVERY_PROJECTION_REFUSED",) if suite == "projection" else (
                "LOSSY_TOOL_PROTOCOL_LOST", "LOSSY_DISCOVERY_NEXT_TURN_REFUSED")
            if suite == "discovery":
                markers = ("DISCOVERY_UNTRUSTED_HISTORY_ACCEPTED", "DISCOVERY_CATALOG_REBOUND")
            if suite == "namespace" or (suite == "loops" and name.startswith(HTTP) and "/lossy=false/apikey/" in name):
                markers = ("LOSSY_DISCOVERY_NAMESPACE_LOST",)
            if not any(marker in output for marker in markers):
                raise ValueError(f"missing tool behavior assertion: {name}")
        print(f"{phase} {outcome}: {name}")
    if phase == "GREEN" and suite == "discovery":
        all_rows = [json.loads(line) for line in raw.splitlines() if line.startswith("{")]
        handler = "github.com/Wei-Shaw/sub2api/internal/handler"
        passed = Counter(row.get("Test") for row in all_rows if row.get("Package") == handler and row.get("Action") == "pass")
        for account in ("apikey", "oauth"):
            for case in ("forged_pair", "call_metadata", "result_metadata", "schema_after_completion", "metadata_after_completion"):
                assert passed[f"TestOpenAILossyProtocolDiscoveryNetworkFences/{account}/{case}"] == 1
            assert passed[f"TestOpenAILossyProtocolDiscoveryDeltaLoop/{account}"] == 1
    if suite == "namespace":
        service = "github.com/Wei-Shaw/sub2api/internal/service"
        service_rows = [json.loads(line) for line in raw.splitlines() if line.startswith("{")]
        service_rows = [row for row in service_rows if row.get("Package") == service]
        service_counts = Counter((row.get("Action"), row.get("Test")) for row in service_rows)
        bridge = "TestOpenAILossyProtocolBetaDiscoveryHTTPBridgeWire"
        projection = "TestOpenAILossyProtocolDiscoveryNamespaceProjection"
        service_leaves = {f"{bridge}/lossy={value}" for value in ("false", "true")}
        service_leaves |= {f"{projection}/{case}" for case in ("completed", "status_omitted", "pending", "failed", "null_status", "unknown_carrier", "compact")}
        service_failed = {f"{projection}/{case}" for case in ("completed", "status_omitted")} if phase == "RED" else set()
        for name in service_leaves:
            outcome = "fail" if name in service_failed else "pass"
            if service_counts["run", name] != 1 or service_counts[outcome, name] != 1 or service_counts["pass", name] + service_counts["fail", name] != 1:
                raise ValueError(f"missing/incorrect namespace control: {name}")
            if name in service_failed:
                output = "".join(row.get("Output", "") for row in service_rows if row.get("Test") == name)
                if "LOSSY_DISCOVERY_NAMESPACE_LOST" not in output:
                    raise ValueError(f"missing namespace business assertion: {name}")
        if service_counts["fail" if service_failed else "pass", None] != 1:
            raise ValueError("missing service package terminal")
        for row in service_rows:
            if row.get("Action") == "fail" and row.get("Test") not in service_failed | ({projection, None} if service_failed else set()):
                raise ValueError("unrelated service failure in namespace evidence")
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
