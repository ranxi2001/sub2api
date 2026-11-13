"""Named HTTP task-rebuild TDD evidence; reuse the reviewed pollution gate."""

import importlib.util
import json
import sys
from collections import Counter
from pathlib import Path


SPEC = importlib.util.spec_from_file_location(
    "readable_evidence", Path(__file__).with_name("assert-readable-continuation.py")
)
COMMON = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(COMMON)
ROOT = "TestOpenAI409HTTPTaskRebuild"
BOUNDARIES = "TestOpenAI409HTTPRebuildBoundaries"
TOOLS = "TestOpenAI409HTTPToolDefinitionOracle"
MIXED = "TestOpenAI409HTTPMixedDeliveryFences"
NORMALIZATION = "TestOpenAI409HTTPControlNormalization"
POINTER_WAIVER = "TestOpenAI409HTTPUnownedPointerMustDetach"
PACKAGE = COMMON.PACKAGE
LEAVES = {
    f"{ROOT}/{account}/stream={stream}/{pointer}/{outcome}"
    for account in ("apikey", "oauth")
    for stream in ("false", "true")
    for pointer in ("intent", "previous", "conversation")
    for outcome in ("completed", "rejected", "write_failed")
}
BOUNDARY_LEAVES = {
    f"{BOUNDARIES}/{account}/stream={stream}/{boundary}"
    for account in ("apikey", "oauth")
    for stream in ("false", "true")
    for boundary in ("pointer_only", "opaque_only", "delivery_pending", "delivery_failed", "b_disabled")
}
TOOL_LEAVES = {
    f"{TOOLS}/{account}/stream={stream}/{mutation}"
    for account in ("apikey", "oauth") for stream in ("false", "true")
    for mutation in ("drop_tools", "change_schema")
}
MIXED_LEAVES = {
    f"{MIXED}/{account}/stream={stream}/{owner}/{state}/{order}"
    for account in ("apikey", "oauth") for stream in ("false", "true")
    for owner in ("A", "B") for state in ("pending", "failed", "conflict")
    for order in ("forward", "reverse")
}
NORMALIZATION_LEAVES = {
    f"{NORMALIZATION}/{account}/stream={stream}/{case}"
    for account, case in (("apikey", "blank_parts"), ("oauth", "blank_parts"), ("oauth", "nested_choice"), ("apikey", "no_tools_parallel"))
    for stream in ("false", "true")
}
POINTER_WAIVER_LEAVES = {f"{POINTER_WAIVER}/setting_error={value}" for value in ("false", "true")}
SUITES = {"boundaries": (BOUNDARIES, BOUNDARY_LEAVES), "tool-oracle": (TOOLS, TOOL_LEAVES), "mixed-fences": (MIXED, MIXED_LEAVES),
          "normalization": (NORMALIZATION, NORMALIZATION_LEAVES), "pointer-waiver": (POINTER_WAIVER, POINTER_WAIVER_LEAVES)}
MUTANTS = {
    "newest-only": f"{MIXED}/apikey/stream=false/A/failed/forward",
    "break-on-owner-mismatch": f"{MIXED}/apikey/stream=false/A/failed/reverse",
}
DIAGNOSTIC = "HTTP_REBUILD_B_DISPATCH_MISSING"
OLD_REFUSALS = (
    "encrypted_agent_recovery_owner_unavailable",
    "encrypted_agent_recovery_http_previous_response_unsupported",
    "encrypted_agent_recovery_wire_changed",
)


def validate(phase, exit_code, transcript, boundaries=False, suite=None, mutant=None):
    if boundaries:
        suite = "boundaries"
    if phase not in {"RED", "GREEN", "MUTANT"} or (suite is not None and (suite not in SUITES or (phase != "GREEN" and not (phase == "RED" and suite in {"normalization", "pointer-waiver"})))):
        raise ValueError("invalid task-rebuild phase")
    if (phase == "MUTANT") != (mutant in MUTANTS) or (mutant is not None and mutant not in MUTANTS):
        raise ValueError("invalid source mutant")
    COMMON.check_clean(transcript)
    events = []
    for line in transcript.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(event, dict):
            raise ValueError("invalid go-test event")
        events.append(event)
    root, leaves = SUITES[suite] if suite else (ROOT, LEAVES)
    if mutant:
        root, leaves = MIXED, {MUTANTS[mutant]}
    terminal = "pass" if phase == "GREEN" else "fail"
    expected_exit = 0 if phase == "GREEN" else 1
    if exit_code != expected_exit:
        raise ValueError("unexpected go-test exit status")
    actions = Counter(
        (event.get("Action"), event.get("Test"))
        for event in events if event.get("Package") == PACKAGE
    )
    for name in leaves | {root}:
        if actions["run", name] != 1:
            raise ValueError(f"missing/repeated named test: {name}")
    for name in leaves | {root, None}:
        if actions[terminal, name] != 1 or actions["pass", name] + actions["fail", name] != 1:
            raise ValueError(f"missing or incorrect test/package terminal: {name}")
    for event in events:
        if event.get("Action") == "fail" and (
            phase == "GREEN" or event.get("Package") != PACKAGE or event.get("Test") not in leaves | {root, None}
        ):
            raise ValueError("unexpected failure outside named task-rebuild RED")
    if phase != "GREEN":
        for name in leaves:
            output = "".join(
                event.get("Output", "") for event in events
                if event.get("Package") == PACKAGE and event.get("Test") == name
            )
            if phase == "MUTANT":
                if "HTTP_REBUILD_FENCE_BYPASSED" not in output or "coding-task-complete" not in output:
                    raise ValueError(f"missing actual extra-generation counterexample: {name}")
            elif suite == "pointer-waiver":
                if "UNOWNED_POINTER_WAIVER_LEAK" not in output:
                    raise ValueError(f"missing actual unowned-pointer dispatch: {name}")
            elif suite == "normalization":
                marker = "automatic next tool turn must finish" if name.endswith("/no_tools_parallel") else DIAGNOSTIC
                code = "encrypted_agent_recovery_readable_intent_missing" if name.endswith("/blank_parts") else "encrypted_agent_recovery_wire_changed"
                if marker not in output or code not in output:
                    raise ValueError(f"missing normalization business refusal: {name}")
            elif DIAGNOSTIC not in output or not any(code in output for code in OLD_REFUSALS):
                raise ValueError(f"missing named B-dispatch refusal assertion: {name}")


def main():
    phase, code, filename, *options = sys.argv[1:]
    if len(options) > 1:
        raise ValueError("unknown option")
    suite, mutant = None, None
    if options:
        if options[0].startswith("--mutant="):
            mutant = options[0].split("=", 1)[1]
        else:
            suite = options[0].removeprefix("--")
            if suite not in SUITES:
                raise ValueError("unknown suite")
    validate(phase, int(code), Path(filename).read_text(), suite=suite, mutant=mutant)
    count = 1 if mutant else len(SUITES[suite][1] if suite else LEAVES)
    print(f"{phase}: {count} named task-rebuild scenarios verified")


if __name__ == "__main__":
    try:
        main()
    except ValueError as exc:
        raise SystemExit(str(exc)) from exc
