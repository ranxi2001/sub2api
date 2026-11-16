"""Parser and source-preparation checks; these are never Go RED/GREEN evidence."""

import importlib.util
import json
import os
import subprocess
import tempfile
import unittest
from contextlib import ExitStack
from pathlib import Path
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location(
    "evidence", Path(__file__).with_name("ws-inherited-model-evidence.py")
)
GATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GATE)
PREPARE_SPEC = importlib.util.spec_from_file_location(
    "prepare", Path(__file__).with_name("prepare-ws-inherited-model-red.py")
)
PREPARE = importlib.util.module_from_spec(PREPARE_SPEC)
PREPARE_SPEC.loader.exec_module(PREPARE)


def transcript(suite="handler", phase="RED"):
    package, tests, red, positives, fences = GATE.contract(suite)
    package = "github.com/Wei-Shaw/sub2api/internal/" + package
    failures = set()
    if red and phase == "RED":
        for leaf in red:
            parts = leaf.split("/")
            failures.update("/".join(parts[:i]) for i in range(1, len(parts) + 1))
        failures.add(None)
    rows = [{"Package": package, "Action": "start"}]
    for name in sorted(tests):
        rows.append({"Package": package, "Action": "run", "Test": name})
        if suite == "pending" and name in positives:
            output = "PENDING_OBSERVATION_LOST: owner_pending=1" if name in failures else "PENDING_OBSERVATION_CONTROL_OK: classification preserved; no ownership publication"
        elif name in red and name in failures:
            output = GATE.MARKER + " T1/T2 completed on A; T2 omitted model; O1/O2 and C delivered; proof=failed; T3 tool_provenance_unknown; A disabled; B sends=0; records unchanged"
        elif name in positives:
            output = "INHERITED_MODEL_TOOL_LOOP_OK: B sends=1; executions=1; A disabled"
        elif name in fences:
            output = "INHERITED_MODEL_FENCE_OK: B sends=0; records unchanged"
        else:
            output = "synthetic parent event"
        rows.append({"Package": package, "Action": "output", "Test": name, "Output": output})
        rows.append({"Package": package, "Action": "fail" if name in failures else "pass", "Test": name})
    rows.append({"Package": package, "Action": "fail" if failures else "pass"})
    return rows


def encode(rows):
    return "\n".join(json.dumps(row) for row in rows) + "\n"


class EvidenceTests(unittest.TestCase):
    def test_complete_contracts(self):
        for suite in ("handler", "guards", "guards_with_client_model", "pending"):
            for phase in ("RED", "GREEN"):
                with self.subTest(suite=suite, phase=phase):
                    status = int(suite in {"handler", "pending"} and phase == "RED")
                    result = GATE.validate(suite, phase, status, encode(transcript(suite, phase)))
                    self.assertEqual(status * len(GATE.contract(suite)[2]), result["behavioral_red_leaves"])

    def test_pending_requires_exact_failure_and_all_error_controls(self):
        for change in ("marker", "missing_backend_error", "failed_control", "skip", "duplicate", "wrong_status"):
            with self.subTest(change=change):
                rows = transcript("pending")
                status = 1
                if change == "marker":
                    for row in rows:
                        row["Output"] = row.get("Output", "").replace("PENDING_OBSERVATION_LOST:", "other failure")
                elif change == "missing_backend_error":
                    rows = [r for r in rows if r.get("Test") != GATE.PENDING_ROOT + "/pending_backend_deadline"]
                elif change == "failed_control":
                    for row in rows:
                        if row.get("Test") == GATE.PENDING_ROOT + "/pending_backend_deadline" and row["Action"] == "pass":
                            row["Action"] = "fail"
                elif change == "skip":
                    rows[3]["Action"] = "skip"
                elif change == "duplicate":
                    rows.append(rows[-1])
                else:
                    status = 0
                with self.assertRaises(ValueError):
                    GATE.validate("pending", "RED", status, encode(rows))

    def test_runtime_build_and_skip_contamination(self):
        for diagnostic in ("[build failed]", "undefined: fixture", "panic: fixture", "WARNING: DATA RACE", "test timed out after 5m", "no tests to run", "--- SKIP: fixture", "signal: killed"):
            for decoded in (False, True):
                with self.subTest(diagnostic=diagnostic, decoded=decoded):
                    rows = transcript()
                    if decoded:
                        rows[2]["Output"] += "\n" + diagnostic
                        raw = encode(rows)
                    else:
                        raw = encode(rows) + diagnostic
                    with self.assertRaises((ValueError, json.JSONDecodeError)):
                        GATE.validate("handler", "RED", 1, raw)

    def test_missing_repeated_unrelated_and_wrong_outcomes(self):
        for mutation in ("missing_control", "duplicate_run", "duplicate_terminal", "unrelated", "package", "skip", "wrong_failure", "missing_marker", "truncated", "empty"):
            with self.subTest(mutation=mutation):
                rows = transcript()
                if mutation == "missing_control":
                    rows = [r for r in rows if r.get("Test") != GATE.FENCES + "/function/pending"]
                elif mutation == "duplicate_run":
                    rows.append(dict(rows[1]))
                elif mutation == "duplicate_terminal":
                    rows.append(dict(rows[-1]))
                elif mutation == "unrelated":
                    rows.append({"Package": rows[0]["Package"], "Action": "fail", "Test": "TestOther"})
                elif mutation == "package":
                    rows[0]["Package"] += "_other"
                elif mutation == "skip":
                    rows[3]["Action"] = "skip"
                elif mutation == "wrong_failure":
                    for row in rows:
                        if row.get("Action") == "pass" and row.get("Test") == GATE.FENCES + "/function/pending":
                            row["Action"] = "fail"
                elif mutation == "missing_marker":
                    for row in rows:
                        row["Output"] = row.get("Output", "").replace(GATE.MARKER, "unrelated assertion")
                elif mutation == "truncated":
                    rows = rows[:-1]
                elif mutation == "empty":
                    rows = []
                with self.assertRaises(ValueError):
                    GATE.validate("handler", "RED", 1, encode(rows))

    def test_status_phase_and_malformed_input(self):
        for status in (0, 2, 137):
            with self.subTest(status=status), self.assertRaises(ValueError):
                GATE.validate("handler", "RED", status, encode(transcript()))
        with self.assertRaises(ValueError):
            GATE.validate("handler", "GREEN", 0, encode(transcript()))
        for raw in ("{malformed\n", "[]\n", "null\n", "not JSON\n"):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                GATE.validate("handler", "RED", 1, raw)


class SourcePreparationTests(unittest.TestCase):
    def isolated_git_environment(self, root):
        env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        env.update(GIT_AUTHOR_NAME="Fixture", GIT_AUTHOR_EMAIL="fixture@example.invalid",
                   GIT_COMMITTER_NAME="Fixture", GIT_COMMITTER_EMAIL="fixture@example.invalid",
                   GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_COUNT="3")
        empty_template = root / "empty-template"
        empty_template.mkdir(exist_ok=True)
        for index, (key, value) in enumerate({
            "commit.gpgsign": "false", "core.hooksPath": os.devnull,
            "init.templateDir": str(empty_template),
        }.items()):
            env[f"GIT_CONFIG_KEY_{index}"] = key
            env[f"GIT_CONFIG_VALUE_{index}"] = value
        return env

    def test_only_pinned_additive_guard_fixture_is_allowed(self):
        for change in ("unchanged", "approved_extension", "new_unapproved_fixture", "dirty_guard", "changed_original_assertion"):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as tmp, ExitStack() as stack:
                root = Path(tmp)
                repo = root / "repo"
                repo.mkdir()
                env = self.isolated_git_environment(root)

                def git(*args, directory=repo):
                    return subprocess.check_output(["git", "-C", str(directory), *args], env=env, stderr=subprocess.PIPE).decode().strip()

                # A real external repository/index must survive both the fixture
                # commands and prepare()'s independently spawned Git processes.
                external = root / "external"
                git("init", "-q", str(external))
                (external / "sentinel").write_text("external work must remain unchanged\n")
                git("add", "sentinel", directory=external)
                git("commit", "-qm", "external baseline", directory=external)
                external_index = external / ".git/index"
                index_before = external_index.read_bytes()
                head_before = (external / ".git/HEAD").read_bytes()
                external_commit_before = git("rev-parse", "HEAD", directory=external)
                rejecting_hook = external / ".git/hooks/pre-commit"
                rejecting_hook.parent.mkdir()
                rejecting_hook.write_text("#!/bin/sh\nexit 99\n")
                rejecting_hook.chmod(0o755)
                untrusted_config = root / "untrusted-gitconfig"
                untrusted_config.write_text(f'[commit]\n gpgsign = true\n[core]\n hooksPath = "{rejecting_hook.parent}"\n')
                stack.enter_context(patch.dict(os.environ, {
                    "GIT_DIR": str(external / ".git"), "GIT_WORK_TREE": str(external),
                    "GIT_INDEX_FILE": str(external_index),
                    "GIT_COMMON_DIR": str(external / ".git"),
                    "GIT_OBJECT_DIRECTORY": str(external / ".git/objects"),
                    "GIT_CONFIG_GLOBAL": str(untrusted_config),
                    "GIT_CONFIG_COUNT": "0",
                }))
                env = self.isolated_git_environment(root)

                def commit(message):
                    git("add", ".")
                    git("commit", "--allow-empty", "-qm", message)
                    return git("rev-parse", "HEAD")

                git("init", "-q")
                inherited_git_dir = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "--absolute-git-dir"], stderr=subprocess.PIPE).decode().strip()
                self.assertEqual((external / ".git").resolve(), Path(inherited_git_dir).resolve(), "contamination is active without isolation")
                self.assertEqual((repo / ".git").resolve(), Path(git("rev-parse", "--absolute-git-dir")).resolve())
                guard = repo / PREPARE.GUARDS
                guard.parent.mkdir(parents=True)
                handler = repo / "backend/internal/handler/existing_test.go"
                handler.parent.mkdir(parents=True)
                handler.write_text("package handler\n")
                original = PREPARE.GUARD_SOURCE_START.decode() + "// identical original assertion and helper bodies\n"
                guard.write_text("package service\n" + original)
                baseline = commit("baseline")
                guard.write_text("package service\n// independent pending fixture\n" + original)
                extension = commit("approved upstream extension")
                fixture = repo / PREPARE.FIXTURE
                fixture.parent.mkdir(parents=True, exist_ok=True)
                fixture.write_text("package handler\n// identical candidate fixture\n")
                if change == "unchanged":
                    guard.write_text("package service\n" + original)
                elif change == "new_unapproved_fixture":
                    guard.write_text(guard.read_text() + "// arbitrary added fixture\n")
                elif change == "changed_original_assertion":
                    guard.write_text(guard.read_text().replace("identical original assertion", "weakened original assertion"))
                    extension = commit("unacceptable approved extension changes existing guards")
                commit("candidate")
                if change == "dirty_guard":
                    guard.write_text(guard.read_text() + "// dirty guard\n")
                destination, evidence = root / "baseline", root / "evidence"
                with patch.dict(os.environ, env, clear=True), patch.object(PREPARE, "BASELINE", baseline), patch.object(PREPARE, "GUARD_EXTENSION", extension):
                    previous = Path.cwd()
                    os.chdir(repo)
                    try:
                        if change in {"unchanged", "approved_extension"}:
                            PREPARE.prepare(destination, evidence)
                            manifest = json.loads((evidence / "source-manifest.json").read_text())
                            self.assertEqual([PREPARE.FIXTURE, PREPARE.GUARDS], manifest["candidate_test_overlays"])
                            self.assertEqual([], manifest["production_changes_in_red"])
                            self.assertEqual(guard.read_bytes(), (destination / PREPARE.GUARDS).read_bytes())
                            self.assertEqual(git("hash-object", str(guard)), manifest["guard_fixture_blob"])
                            self.assertEqual(git("rev-parse", baseline + ":" + PREPARE.GUARDS), manifest["baseline_guard_fixture_blob"])
                            self.assertEqual(fixture.read_bytes(), (destination / PREPARE.FIXTURE).read_bytes())
                            self.assertEqual(extension if change == "approved_extension" else None, manifest["approved_guard_extension"])
                        else:
                            with self.assertRaisesRegex(ValueError, {
                                "new_unapproved_fixture": "outside the pinned upstream extension",
                                "dirty_guard": "dirty candidate guard",
                                "changed_original_assertion": "identical baseline/candidate guard tests",
                            }[change]):
                                PREPARE.prepare(destination, evidence)
                    finally:
                        os.chdir(previous)
                self.assertEqual(index_before, external_index.read_bytes(), "external Git index must not be touched")
                self.assertEqual(head_before, (external / ".git/HEAD").read_bytes())
                self.assertEqual(external_commit_before, git("rev-parse", "HEAD", directory=external))
                self.assertEqual("external work must remain unchanged\n", (external / "sentinel").read_text())


if __name__ == "__main__":
    unittest.main()
