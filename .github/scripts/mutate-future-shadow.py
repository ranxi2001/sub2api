"""CI-only counterexample: latest task per session despite committed item owners."""

import hashlib
import json
import os
import sys
from pathlib import Path


def main():
    root = Path(sys.argv[1]).resolve()
    temporary = os.environ.get("RUNNER_TEMP")
    if os.environ.get("GITHUB_ACTIONS") != "true" or not temporary or not root.is_relative_to(Path(temporary).resolve()):
        raise SystemExit("source mutations require a disposable Actions RUNNER_TEMP checkout")
    changes = (
        ("backend/internal/service/openai_409_readable_shadow.go",
         "r.groupID, r.shadowModel, r.session, hex.EncodeToString(item[:])",
         'r.groupID, r.shadowModel, r.session, "session-latest-task"'),
        ("backend/internal/repository/gateway_cache_readable_shadow.go",
         "local old = redis.call('GET', KEYS[1])", "local old = false"),
    )
    for name, anchor, replacement in changes:
        target = root / name
        original = target.read_text()
        if original.count(anchor) != 1:
            raise SystemExit(f"mutation anchor changed: {name}")
        mutated = original.replace(anchor, replacement)
        target.write_text(mutated)
        print(json.dumps({"mutation": "session-last-task", "target": name,
                          "candidate_sha256": hashlib.sha256(original.encode()).hexdigest(),
                          "mutant_sha256": hashlib.sha256(mutated.encode()).hexdigest()}))


if __name__ == "__main__":
    main()
