"""CI-only evidence gate: compilation failures, skips and empty runs are not RED."""

import json
import sys
from pathlib import Path


def main():
    phase, exit_code, filename = sys.argv[1:]
    if phase not in {"RED", "GREEN"}:
        raise SystemExit("unknown phase")
    events = []
    for line in Path(filename).read_text().splitlines():
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            # Compiler diagnostics may be non-JSON; the required test events
            # below still reject compilation-only failures.
            continue

    root = "TestOpenAI409ReadableContinuationHTTP"
    leaves = {
        f"{root}/{fixture}/stream={stream}/{outcome}"
        for fixture in ("summary", "content", "stored_content")
        for stream in ("false", "true")
        for outcome in ("completed", "rejected", "write_failed")
    }
    actions = {(event.get("Action"), event.get("Test")) for event in events}
    if any(event.get("Action") == "skip" for event in events):
        raise SystemExit("skipped tests cannot prove RED/GREEN")
    if ("run", root) not in actions or not all(("run", name) in actions for name in leaves):
        raise SystemExit("missing named HTTP behavior tests (build/no-tests is not evidence)")

    if phase == "GREEN":
        if int(exit_code) != 0 or ("pass", root) not in actions:
            raise SystemExit("candidate HTTP regression did not pass")
        if not all(("pass", name) in actions for name in leaves):
            raise SystemExit("not all candidate HTTP scenarios passed")
    else:
        if int(exit_code) == 0 or ("fail", root) not in actions:
            raise SystemExit("pinned production did not fail the behavior test")
        for fixture in ("summary", "content", "stored_content"):
            for stream in ("false", "true"):
                name = f"{root}/{fixture}/stream={stream}/completed"
                output = "".join(
                    event.get("Output", "") for event in events if event.get("Test") == name
                )
                if ("fail", name) not in actions or "encrypted_agent_recovery_wire_changed" not in output:
                    raise SystemExit(f"missing named input-integrity business failure: {name}")
    print(f"{phase}: named HTTP JSON/SSE behavior evidence accepted ({len(leaves)} scenarios)")


if __name__ == "__main__":
    main()
