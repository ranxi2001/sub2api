"""Materialize the entire pinned baseline plus one identical candidate test.

No historical overlay helper or existing worktree is modified. All archived
backend blobs (including dependencies and historical fixtures) are verified.
"""

import json
import shutil
import subprocess
import sys
from pathlib import Path

BASELINE = "da526bfbab47efd40549cab98f1069f9b2b45b9f"
FIXTURE = "backend/internal/handler/openai_ws_inherited_model_test.go"
GUARDS = "backend/internal/service/openai_ws_owner_conflict_test.go"


def git(*args, **kwargs):
    return subprocess.check_output(["git", *args], **kwargs)


def prepare(destination, evidence):
    destination, evidence = Path(destination).resolve(), Path(evidence).resolve()
    candidate = git("rev-parse", "HEAD").decode().strip()
    # A fresh directory avoids deleting or changing another writer's files.
    destination.mkdir(parents=True, exist_ok=False)
    evidence.mkdir(parents=True, exist_ok=True)
    archive = subprocess.Popen(["git", "archive", BASELINE], stdout=subprocess.PIPE)
    try:
        subprocess.run(["tar", "-x", "-C", str(destination)], stdin=archive.stdout, check=True)
    finally:
        archive.stdout.close()
    if archive.wait() != 0:
        raise RuntimeError("baseline archive failed")
    entries = []
    for row in git("ls-tree", "-rz", BASELINE, "--", "backend").split(b"\0"):
        if not row:
            continue
        metadata, path = row.decode().split("\t", 1)
        mode, kind, blob = metadata.split()
        if kind != "blob" or mode not in {"100644", "100755"}:
            raise ValueError(f"unhandled baseline entry: {path}")
        entries.append((path, blob))
    paths = "".join(str(destination / path) + "\n" for path, _ in entries)
    actual = git("hash-object", "--stdin-paths", input=paths.encode()).decode().splitlines()
    if actual != [blob for _, blob in entries]:
        raise ValueError("baseline backend blob mismatch")
    if (destination / FIXTURE).exists():
        raise ValueError("fixture unexpectedly exists in pinned baseline")
    fixture_blob = git("rev-parse", f"{candidate}:{FIXTURE}").decode().strip()
    if git("hash-object", FIXTURE).decode().strip() != fixture_blob:
        raise ValueError("dirty candidate fixture")
    if Path(GUARDS).read_bytes() != (destination / GUARDS).read_bytes():
        raise ValueError("guard fixture changed; identical baseline/candidate guard tests required")
    shutil.copyfile(FIXTURE, destination / FIXTURE)
    if git("hash-object", str(destination / FIXTURE)).decode().strip() != fixture_blob:
        raise ValueError("candidate test copy mismatch")
    manifest = {
        "baseline": BASELINE, "candidate": candidate,
        "baseline_tree": git("rev-parse", f"{BASELINE}^{{tree}}").decode().strip(),
        "baseline_backend_tree": git("rev-parse", f"{BASELINE}:backend").decode().strip(),
        "candidate_fixture_blob": fixture_blob, "fixture": FIXTURE,
        "guard_fixture_blob": git("hash-object", GUARDS).decode().strip(),
        "verified_baseline_backend_blobs": len(entries),
        "production_changes_in_red": [], "candidate_test_overlays": [FIXTURE],
    }
    (evidence / "source-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    (evidence / "baseline-backend-blobs.txt").write_text("".join(f"{blob} {path}\n" for path, blob in entries))
    print(json.dumps(manifest, sort_keys=True))


if __name__ == "__main__":
    prepare(*sys.argv[1:])
