"""Accept only the complete authenticated inherited-model behavioral matrix."""

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

POSITIVE = "TestOpenAIWSInheritedModelToolProof"
FENCES = "TestOpenAIWSInheritedModelProofFences"
SCENARIOS = ("explicit", "inherited", "mapped_explicit", "mapped_inherited")
NEGATIVES = (
    "unknown", "pending", "epoch", "arguments", "terminal_excludes",
    "model_change", "same_wire_model_change",
)
KINDS = ("function", "custom")
MARKER = "INHERITED_MODEL_TOOL_PROOF_NOT_RESUMED:"
PENDING_ROOT = "TestOpenAIAgentPendingObservation"
PENDING_CASES = (
    "pending_get_deadline", "pending_get_cancel", "latest_pending_get_deadline", "pending_timer_deadline",
    "initial_backend_error", "initial_backend_deadline", "pending_backend_error", "pending_backend_deadline",
    "completed", "failed", "conflicted", "missing",
)


def contract(suite):
    if suite == "pending":
        leaves = {f"{PENDING_ROOT}/{case}" for case in PENDING_CASES}
        red = {f"{PENDING_ROOT}/{case}" for case in ("pending_get_deadline", "pending_get_cancel", "latest_pending_get_deadline")}
        return "service", leaves | {PENDING_ROOT}, red, leaves, set()
    if suite == "handler":
        roots = {POSITIVE, FENCES}
        parents = {f"{root}/{kind}" for root in roots for kind in KINDS}
        good = {f"{POSITIVE}/{kind}/{case}" for kind in KINDS for case in SCENARIOS}
        fences = {f"{FENCES}/{kind}/{case}" for kind in KINDS for case in NEGATIVES}
        red = {name for name in good if name.endswith("/inherited") or name.endswith("/mapped_inherited")}
        return "handler", roots | parents | good | fences, red, good, fences
    if suite in {"guards", "guards_with_client_model"}:
        cases = {
            "TestOpenAIToolProvenanceDeliveryAndScope": (
                "completed", "early_only", "write_failed", "terminal_excludes", "duplicate",
            ),
            "TestOpenAIWSMixedOwnerFinalSendFences": (
                "none", "pending", "failed", "conflicted", "missing", "changed", "input", "tools", "call_proof",
            ),
        }
        if suite == "guards_with_client_model":
            cases["TestOpenAIWSRecoveryEffectiveClientModel"] = (
                "no_ws_preflight", "inherited", "explicit_switch", "inherited_after_switch",
                "explicit_overrides_preflight", "separate_connection",
            )
        tests = set(cases) | {f"{root}/{case}" for root, leaves in cases.items() for case in leaves}
        return "service", tests, set(), set(), set()
    raise ValueError("unknown suite")


def validate(suite, phase, status, transcript):
    if phase not in {"RED", "GREEN"}:
        raise ValueError("unknown phase")
    CLEAN.check_clean(transcript)
    package, tests, red_leaves, positives, fences = contract(suite)
    package = "github.com/Wei-Shaw/sub2api/internal/" + package
    rows = []
    for line in transcript.splitlines():
        # Go can print dependency downloads on stderr before its JSON stream.
        if line.startswith("go: downloading "):
            continue
        row = json.loads(line)
        if not isinstance(row, dict) or row.get("Package") != package:
            raise ValueError("unexpected package or malformed event")
        if row.get("Action") not in {"start", "run", "pause", "cont", "output", "pass", "fail"}:
            raise ValueError("unexpected action (including skip/build failure)")
        if row.get("Test") not in tests | {None}:
            raise ValueError("unrelated test")
        rows.append(row)
    counts = Counter((r.get("Action"), r.get("Test")) for r in rows)
    failures = set()
    if red_leaves and phase == "RED":
        for leaf in red_leaves:
            parts = leaf.split("/")
            failures.update("/".join(parts[:i]) for i in range(1, len(parts) + 1))
        failures.add(None)
    if status != (1 if failures else 0):
        raise ValueError("wrong process exit status")
    if counts["start", None] != 1 or {r.get("Test") for r in rows if r.get("Action") == "run"} != tests:
        raise ValueError("missing/repeated package or test run")
    for name in tests | {None}:
        if name is not None and counts["run", name] != 1:
            raise ValueError(f"missing/repeated run: {name}")
        expected = "fail" if name in failures else "pass"
        if counts[expected, name] != 1 or counts["pass", name] + counts["fail", name] != 1:
            raise ValueError(f"wrong/missing/repeated outcome: {name}")
    for name in positives | fences:
        output = "".join(r.get("Output", "") for r in rows if r.get("Test") == name)
        if suite == "pending":
            required = ("PENDING_OBSERVATION_LOST:", "owner_pending=1") if name in failures else (
                "PENDING_OBSERVATION_CONTROL_OK:", "classification preserved", "no ownership publication",
            )
        elif name in failures:
            required = (
                MARKER, "T1/T2 completed on A", "T2 omitted model", "O1/O2 and C delivered",
                "proof=failed", "T3 tool_provenance_unknown", "A disabled", "B sends=0", "records unchanged",
            )
        elif name in fences:
            required = ("INHERITED_MODEL_FENCE_OK:", "B sends=0", "records unchanged")
        else:
            required = ("INHERITED_MODEL_TOOL_LOOP_OK:", "B sends=1", "executions=1", "A disabled")
        if any(text not in output for text in required):
            raise ValueError(f"missing behavioral evidence: {name}")
    return {"suite": suite, "phase": phase, "exit_status": status, "tests": len(tests), "behavioral_red_leaves": len(red_leaves) if failures else 0}


if __name__ == "__main__":
    suite, phase, status, path = sys.argv[1:]
    print(json.dumps(validate(suite, phase, int(status), Path(path).read_text()), sort_keys=True))
