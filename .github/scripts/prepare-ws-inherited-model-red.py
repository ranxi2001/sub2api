"""Materialize the pinned baseline plus identical candidate handler/guard tests.

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
# This upstream commit only prepends the independent pending-deadline fixture.
# Keep its full blob pinned; arbitrary changes to the candidate guards still fail.
GUARD_EXTENSION = "9dc66846a16609fb75b59a643563fae3284944cf"
GUARD_SOURCE_START = b"// Calls are learned only from the real response observer and successful write\n"


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
    guard_blob = git("rev-parse", f"{candidate}:{GUARDS}").decode().strip()
    if git("hash-object", GUARDS).decode().strip() != guard_blob:
        raise ValueError("dirty candidate guard fixture")
    baseline_guards = (destination / GUARDS).read_bytes()
    candidate_guards = Path(GUARDS).read_bytes()
    if candidate_guards != baseline_guards:
        approved_guards = git("show", f"{GUARD_EXTENSION}:{GUARDS}")
        if candidate_guards != approved_guards:
            raise ValueError("guard fixture changed outside the pinned upstream extension")
        if (baseline_guards.count(GUARD_SOURCE_START) != 1
                or candidate_guards.count(GUARD_SOURCE_START) != 1
                or baseline_guards.split(GUARD_SOURCE_START, 1)[1]
                != candidate_guards.split(GUARD_SOURCE_START, 1)[1]):
            raise ValueError("guard fixture changed; identical baseline/candidate guard tests required")
    # Overlay both committed candidate fixtures so RED and GREEN run identical
    # selected handler/guard assertions. Guard approval above remains pinned.
    for path, blob in ((FIXTURE, fixture_blob), (GUARDS, guard_blob)):
        shutil.copyfile(path, destination / path)
        if git("hash-object", str(destination / path)).decode().strip() != blob:
            raise ValueError("candidate test copy mismatch: " + path)
    manifest = {
        "baseline": BASELINE, "candidate": candidate,
        "baseline_tree": git("rev-parse", f"{BASELINE}^{{tree}}").decode().strip(),
        "baseline_backend_tree": git("rev-parse", f"{BASELINE}:backend").decode().strip(),
        "candidate_fixture_blob": fixture_blob, "fixture": FIXTURE,
        "guard_fixture_blob": guard_blob,
        "baseline_guard_fixture_blob": git("rev-parse", f"{BASELINE}:{GUARDS}").decode().strip(),
        "approved_guard_extension": GUARD_EXTENSION if candidate_guards != baseline_guards else None,
        "verified_baseline_backend_blobs": len(entries),
        "production_changes_in_red": [], "candidate_test_overlays": [FIXTURE, GUARDS],
    }
    (evidence / "source-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    (evidence / "baseline-backend-blobs.txt").write_text("".join(f"{blob} {path}\n" for path, blob in entries))
    print(json.dumps(manifest, sort_keys=True))


if __name__ == "__main__":
    prepare(*sys.argv[1:])
