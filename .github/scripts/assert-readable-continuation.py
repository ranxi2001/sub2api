"""CI-only evidence gate: compilation failures, skips and empty runs are not RED."""

import json
import re
import sys
from collections import Counter
from pathlib import Path


ROOT = "TestOpenAI409ReadableContinuationHTTP"
PACKAGE = "github.com/Wei-Shaw/sub2api/internal/handler"
LEAVES = {
    f"{ROOT}/{fixture}/stream={stream}/{outcome}"
    for fixture in ("summary", "content", "stored_content")
    for stream in ("false", "true")
    for outcome in ("completed", "rejected", "write_failed")
}
POLLUTION = re.compile(
    r"panic|data\s+race|race\s+detected|race\s+detector|ThreadSanitizer|"
    r"fatal\s+(?:error|runtime)|runtime\s+error|runtime:\s*out of memory|"
    r"SIG(?:SEGV|ABRT|BUS|ILL|QUIT)|segmentation\s+(?:fault|violation)|"
    r"stack\s+overflow|core\s+dumped|crash|test\s+timed\s+out|"
    r"signal:\s*(?:killed|aborted|terminated|interrupt)|\bKilled\b|"
    r"build\s+failed|setup\s+failed|undefined:|no\s+tests\s+to\s+run|"
    r"---\s*SKIP:|\[SKIP\]",
    re.IGNORECASE,
)


def check_clean(transcript):
    # Inspect raw stderr AND decoded go-test Output. A recovered panic can be
    # a structured application log inside JSON, even with exit=0 and PASS.
    outputs = []
    for line in transcript.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(event, dict):
            if event.get("Action") == "skip":
                raise ValueError("skipped tests cannot prove RED/GREEN")
            output = event.get("Output", "")
            if isinstance(output, str):
                outputs.append(output)
    if POLLUTION.search(transcript) or POLLUTION.search("".join(outputs)):
        raise ValueError("contaminated evidence: runtime/build/skip diagnostic")


def validate(phase, exit_code, transcript):
    if phase not in {"RED", "GREEN"}:
        raise ValueError("unknown phase")
    check_clean(transcript)
    events = []
    for line in transcript.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            # Raw stderr was scanned above. Required test/package terminal
            # events below reject truncated and compilation-only evidence.
            continue
        if not isinstance(event, dict):
            raise ValueError("invalid go-test event")
        events.append(event)

    actions = Counter(
        (event.get("Action"), event.get("Test"))
        for event in events
        if event.get("Package") == PACKAGE
    )
    for name in LEAVES | {ROOT}:
        if actions["run", name] != 1:
            raise ValueError("missing or repeated named HTTP behavior test")
        if actions["pass", name] + actions["fail", name] != 1:
            raise ValueError("missing or repeated test terminal event")
    for event in events:
        if event.get("Action") == "fail" and (
            event.get("Package") != PACKAGE or event.get("Test") not in LEAVES | {ROOT, None}
        ):
            raise ValueError("unrelated failure in HTTP evidence")
    expected = "pass" if phase == "GREEN" else "fail"
    if actions[expected, None] != 1 or actions["pass", None] + actions["fail", None] != 1:
        raise ValueError("missing or incorrect package terminal event")

    if phase == "GREEN":
        if exit_code != 0 or actions["pass", ROOT] != 1:
            raise ValueError("candidate HTTP regression did not pass")
        if not all(actions["pass", name] == 1 for name in LEAVES):
            raise ValueError("not all candidate HTTP scenarios passed")
        if any(event.get("Action") == "fail" for event in events):
            raise ValueError("unexpected failure in GREEN evidence")
    else:
        if exit_code != 1 or actions["fail", ROOT] != 1:
            raise ValueError("pinned production did not fail the behavior test")
        for fixture in ("summary", "content", "stored_content"):
            for stream in ("false", "true"):
                name = f"{ROOT}/{fixture}/stream={stream}/completed"
                output = "".join(
                    event.get("Output", "") for event in events
                    if event.get("Package") == PACKAGE and event.get("Test") == name
                )
                if actions["fail", name] != 1 or "encrypted_agent_recovery_wire_changed" not in output:
                    raise ValueError(f"missing named input-integrity business failure: {name}")


def main():
    if len(sys.argv) == 3 and sys.argv[1] == "--clean-log":
        check_clean(Path(sys.argv[2]).read_text())
        print("No runtime/build/skip contamination found")
        return
    phase, exit_code, filename = sys.argv[1:]
    validate(phase, int(exit_code), Path(filename).read_text())
    print(f"{phase}: named HTTP JSON/SSE behavior evidence accepted ({len(LEAVES)} scenarios)")


if __name__ == "__main__":
    try:
        main()
    except ValueError as exc:
        raise SystemExit(str(exc)) from exc
