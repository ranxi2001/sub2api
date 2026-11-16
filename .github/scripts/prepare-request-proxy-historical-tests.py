"""Isolate twelve PR885 fixtures and restore two existing historical unit fixtures."""

import json
import os
import re
import subprocess
import sys
from pathlib import Path


BASELINE_SHA = "94bb4a53e9e5057990cd851ab0620d99b116ad41"
CONTRACT = "backend/internal/service/openai_proxy_routing.go"
REDIS_HELPER = "backend/internal/testutil/redissession/store.go"
FIXTURES = (
    "backend/internal/service/admin_account_request_proxy_upstream_merge_test.go",
    "backend/internal/service/openai_codex_models_request_proxy_test.go",
    "backend/internal/service/openai_excel_bps_proxy_config_test.go",
    "backend/internal/service/openai_excel_bps_request_proxy_test.go",
    "backend/internal/service/openai_maintenance_remaining_proxy_test.go",
    "backend/internal/service/openai_maintenance_task_request_proxy_test.go",
    "backend/internal/service/openai_oauth_create_request_proxy_test.go",
    "backend/internal/service/openai_oauth_reauth_request_proxy_test.go",
    "backend/internal/service/openai_oauth_request_proxy_test.go",
    "backend/internal/service/openai_proxy_routing_test.go",
    "backend/internal/service/openai_quota_request_proxy_test.go",
    "backend/internal/service/openai_request_proxy_test.go",
)
ORACLES = (
    "backend/internal/service/openai_409_ws_ticket_test.go",
    "backend/internal/handler/openai_409_ws_rebuild_test.go",
    "backend/internal/handler/openai_409_ws_oracle_test.go",
)
ADAPTED_FIXTURES = {
    "backend/internal/service/openai_excel_bps_warm_test.go": "9dc2054fd0ab61d0465e1eb12ac060360bc80166",
    "backend/internal/service/admin_service_list_users_test.go": "12597f3f10ff967818cd647bc293d8f27185136c",
}


def git(tree, *args):
    return subprocess.run(
        ["git", "-C", str(tree), *args], check=True, capture_output=True, text=True, timeout=30
    ).stdout.strip()


def entry(tree, revision, relative):
    raw = git(tree, "ls-tree", revision, "--", relative)
    if not raw:
        return None
    metadata, path = raw.split("\t", 1)
    mode, kind, blob = metadata.split()
    if path != relative or mode != "100644" or kind != "blob":
        raise ValueError("unexpected git entry: " + relative)
    return blob


def safe_path(root, relative):
    path = root
    for part in Path(relative).parts:
        if part in {"..", "."}:
            raise ValueError("unexpected relative path")
        path = path / part
        if path.is_symlink():
            raise ValueError("symlink refused: " + str(path))
    return path


def candidate_file(tree, candidate, relative):
    blob = entry(tree, candidate, relative)
    path = safe_path(tree, relative)
    if not blob or not path.is_file() or git(tree, "hash-object", "--", str(path)) != blob:
        raise ValueError("missing or dirty candidate file: " + relative)
    return blob, path


def prepare(baseline, root, candidate, destination):
    # No CLI baseline override: the helper owns exactly this historical boundary.
    if baseline != BASELINE_SHA:
        raise ValueError("unsupported historical baseline")
    if not re.fullmatch(r"[0-9a-f]{40}", candidate):
        raise ValueError("candidate must be an exact commit SHA")
    tree = Path(os.path.abspath(root))
    if tree.is_symlink() or not tree.is_dir():
        raise ValueError("invalid or symlinked worktree")
    tree = tree.resolve()
    if Path(git(tree, "rev-parse", "--show-toplevel")).resolve() != tree:
        raise ValueError("expected the worktree root")
    if git(tree, "rev-parse", "HEAD") != candidate:
        raise ValueError("worktree HEAD is not candidate")
    if git(tree, "rev-parse", baseline + "^{commit}") != baseline:
        raise ValueError("baseline is not an exact commit")

    manifest = Path(os.path.abspath(destination))
    safe_path(Path(manifest.anchor), str(manifest.relative_to(manifest.anchor)))
    if os.path.lexists(manifest) or not manifest.parent.is_dir() or manifest.is_relative_to(tree):
        raise ValueError("manifest must be a new external evidence file")

    # The baseline lacks the actual production contract, not just a test name.
    contract_path = safe_path(tree, CONTRACT)
    contract_blob = entry(tree, candidate, CONTRACT)
    if entry(tree, baseline, CONTRACT) or not contract_blob or os.path.lexists(contract_path):
        raise ValueError("request proxy contract must be absent in historical production")
    contract_source = git(tree, "show", candidate + ":" + CONTRACT)
    if "OpenAIProxyModeKey" not in contract_source or "OpenAIRequestProxyEnabled" not in contract_source:
        raise ValueError("candidate request proxy contract is unexpected")

    # Preserve the previously proved production baseline plus its sole explicit
    # Redis test-support exception. Do not restore candidate production APIs.
    if entry(tree, baseline, REDIS_HELPER):
        raise ValueError("unexpected historical Redis test helper")
    helper_blob, _ = candidate_file(tree, candidate, REDIS_HELPER)
    production = git(tree, "diff", "--name-only", baseline, "--", "backend/**/*.go", ":!backend/**/*_test.go")
    if production.splitlines() != [REDIS_HELPER]:
        raise ValueError("unexpected production changes")
    if git(tree, "ls-files", "--others", "--", "backend/**/*.go"):
        raise ValueError("unexpected untracked backend Go files")

    oracles = {}
    for relative in ORACLES:
        blob, _ = candidate_file(tree, candidate, relative)
        oracles[relative] = blob

    pending = []
    for relative in FIXTURES:
        if entry(tree, baseline, relative):
            raise ValueError("fixture is not candidate-only: " + relative)
        blob, path = candidate_file(tree, candidate, relative)
        pending.append((relative, blob, path))

    adaptations = []
    for relative, expected_baseline_blob in ADAPTED_FIXTURES.items():
        baseline_blob = entry(tree, baseline, relative)
        if baseline_blob != expected_baseline_blob:
            raise ValueError("unexpected adapted fixture baseline blob: " + relative)
        candidate_blob, path = candidate_file(tree, candidate, relative)
        historical = subprocess.run(
            ["git", "-C", str(tree), "show", baseline + ":" + relative],
            check=True, capture_output=True, timeout=30,
        ).stdout
        adaptations.append((relative, candidate_blob, baseline_blob, path, historical))

    # Preflight every path and blob before the first mutation. A later dirty
    # fixture cannot leave an earlier fixture removed on a rejected preparation.
    records = []
    for relative, blob, path in pending:
        path.unlink()
        records.append({"path": relative, "candidate_blob": blob, "baseline_blob": "absent", "actual": "absent"})
    adapted_records = []
    for relative, candidate_blob, baseline_blob, path, historical in adaptations:
        path.write_bytes(historical)
        actual_blob = git(tree, "hash-object", "--", str(path))
        if actual_blob != baseline_blob:
            raise ValueError("adapted fixture restoration mismatch: " + relative)
        adapted_records.append({"path": relative, "candidate_blob": candidate_blob,
                                "baseline_blob": baseline_blob, "actual_blob": actual_blob})
    result = {
        "baseline": baseline,
        "candidate": candidate,
        "contract": {"path": CONTRACT, "baseline_blob": "absent", "candidate_blob": contract_blob, "actual": "absent"},
        "production_exception": {"path": REDIS_HELPER, "candidate_blob": helper_blob},
        "unchanged_oracles": oracles,
        "fixtures": records,
        "adapted_fixtures": adapted_records,
    }
    with manifest.open("x") as stream:
        json.dump(result, stream, sort_keys=True, indent=2)
        stream.write("\n")
    return result


if __name__ == "__main__":
    if len(sys.argv) != 5:
        raise SystemExit("usage: prepare-request-proxy-historical-tests.py BASELINE TREE CANDIDATE MANIFEST")
    try:
        result = prepare(*sys.argv[1:])
    except (ValueError, OSError, subprocess.SubprocessError) as exc:
        raise SystemExit("historical request-proxy preparation refused: " + str(exc)) from exc
    print("historical request-proxy fixtures isolated: " + str(len(result["fixtures"])))
