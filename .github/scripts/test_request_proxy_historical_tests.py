"""Real temporary Git checks for the fixed historical proxy fixture boundary."""

import importlib.util
import json
import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("prepare-request-proxy-historical-tests.py")
SPEC = importlib.util.spec_from_file_location("historical_proxy", SCRIPT)
HELPER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(HELPER)


class HistoricalProxyFixtureTests(unittest.TestCase):
    def git(self, *args):
        return subprocess.run(
            ["git", "-C", str(self.tree), "-c", "user.name=CI fixture", "-c", "user.email=fixture@example.invalid",
             "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + os.devnull, "-c", "init.templateDir=", *args],
            env=self.env, check=True, capture_output=True, text=True, timeout=15,
        ).stdout.strip()

    def write(self, relative, content):
        path = self.tree / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
        return path

    def make_repository(self, baseline_fixture=False, baseline_contract=False):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        directory = Path(temporary.name).resolve()
        self.tree = directory / "tree"
        self.tree.mkdir()
        self.manifest = directory / "evidence" / "manifest.json"
        self.manifest.parent.mkdir()
        self.env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        self.env.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull)
        self.git("init", "-q")
        self.production = "backend/internal/service/production.go"
        self.write(self.production, "package service // historical production\n")
        for path in HELPER.ORACLES:
            self.write(path, "package service // historical selected oracle\n")
        adapted_paths = tuple(HELPER.ADAPTED_FIXTURES)
        self.adapted_historical = b"package service // historical preserved fixture\n\n"
        for path in adapted_paths:
            self.write(path, self.adapted_historical.decode())
        if baseline_fixture:
            self.write(HELPER.FIXTURES[-1], "package service // preexisting fixture\n")
        if baseline_contract:
            self.write(HELPER.CONTRACT, "package service // preexisting contract\n")
        self.git("add", ".")
        self.git("commit", "-qm", "baseline")
        self.baseline = self.git("rev-parse", "HEAD")
        adapted = {path: self.git("rev-parse", self.baseline + ":" + path) for path in adapted_paths}
        adapted_patch = mock.patch.object(HELPER, "ADAPTED_FIXTURES", adapted)
        adapted_patch.start()
        self.addCleanup(adapted_patch.stop)
        self.write(self.production, "package service // candidate production\n")
        self.write(HELPER.CONTRACT, "package service\nconst OpenAIProxyModeKey = 1\nfunc OpenAIRequestProxyEnabled() {}\n")
        self.write(HELPER.REDIS_HELPER, "package redissession // explicit candidate support\n")
        for path in HELPER.FIXTURES:
            self.write(path, "package service // candidate fixture " + path + "\n")
        for path in adapted_paths:
            self.write(path, "package service // candidate extended fixture\n")
        for path in HELPER.ORACLES:
            self.write(path, "package service // candidate selected oracle " + path + "\n")
        self.git("add", ".")
        self.git("commit", "-qm", "candidate")
        self.candidate = self.git("rev-parse", "HEAD")
        self.write(self.production, self.git("show", self.baseline + ":" + self.production) + "\n")
        (self.tree / HELPER.CONTRACT).unlink()
        patch = mock.patch.object(HELPER, "BASELINE_SHA", self.baseline)
        patch.start()
        self.addCleanup(patch.stop)

    def run_helper(self, baseline=None, candidate=None):
        with mock.patch.dict(os.environ, self.env, clear=True):
            return HELPER.prepare(baseline or self.baseline, self.tree, candidate or self.candidate, self.manifest)

    def test_literal_scope_and_workflow_handoff(self):
        self.assertEqual("94bb4a53e9e5057990cd851ab0620d99b116ad41", HELPER.BASELINE_SHA)
        self.assertEqual(12, len(HELPER.FIXTURES))
        self.assertEqual(12, len(set(HELPER.FIXTURES)))
        self.assertFalse(set(HELPER.FIXTURES) & set(HELPER.ORACLES))
        self.assertEqual({"backend/internal/service/openai_excel_bps_warm_test.go":
                          "9dc2054fd0ab61d0465e1eb12ac060360bc80166"}, HELPER.ADAPTED_FIXTURES)
        self.assertFalse(set(HELPER.ADAPTED_FIXTURES) & (set(HELPER.FIXTURES) | set(HELPER.ORACLES)))
        root = SCRIPT.resolve().parents[2]
        workflow = (root / ".github/workflows/ws-readable-continuation.yml").read_text()
        self.assertIn("- '.github/scripts/prepare-request-proxy-historical-tests.py'", workflow)
        self.assertIn("- '.github/scripts/test_request_proxy_historical_tests.py'", workflow)
        self.assertIn("-p 'test_request_proxy_historical_tests.py'", workflow)
        residual = workflow.split("      - name: Residual headers tools and ticket actual transport counterexamples\n", 1)[1].split("      - name:", 1)[0]
        invocation = 'python3 .github/scripts/prepare-request-proxy-historical-tests.py "$baseline" "$tested_tree" "$CANDIDATE_SHA" "$RUNNER_TEMP/ws-evidence/request-proxy-historical-tests.json"'
        self.assertEqual(1, residual.count(invocation))
        self.assertRegex(residual, r"(?s)if \[ \"\$PHASE\" = RED \]; then.*" + re.escape(invocation) + r"\n          fi")
        self.assertIn("path: ${{ runner.temp }}/ws-evidence/", workflow)
        inline = re.search(r"for target in (backend/.*?); do", residual).group(1).split()
        owned = set(HELPER.FIXTURES) | set(HELPER.ADAPTED_FIXTURES)
        self.assertFalse(set(inline) & owned, "one historical owner per fixture")
        for script in (root / ".github/scripts").glob("*.sh"):
            source = script.read_text()
            self.assertFalse(any(path in source for path in owned), str(script))

    def test_valid_preparation_removes_twelve_and_restores_one_fixture(self):
        self.make_repository()
        before = {str(path.relative_to(self.tree)): path.read_bytes() for path in (self.tree / "backend").rglob("*.go")}
        result = self.run_helper()
        after = {str(path.relative_to(self.tree)): path.read_bytes() for path in (self.tree / "backend").rglob("*.go")}
        self.assertEqual(set(HELPER.FIXTURES), set(before) - set(after))
        expected = {path: content for path, content in before.items() if path not in HELPER.FIXTURES}
        for path in HELPER.ADAPTED_FIXTURES:
            expected[path] = self.adapted_historical
        self.assertEqual(expected, after)
        self.assertEqual(result, json.loads(self.manifest.read_text()))
        self.assertEqual(self.baseline, result["baseline"])
        self.assertEqual(self.candidate, result["candidate"])
        self.assertEqual(HELPER.REDIS_HELPER, self.git("diff", "--name-only", self.baseline, "--", "backend/**/*.go", ":!backend/**/*_test.go"))
        for record in result["fixtures"]:
            self.assertEqual(self.git("rev-parse", self.candidate + ":" + record["path"]), record["candidate_blob"])
            self.assertEqual("absent", record["baseline_blob"])
            self.assertEqual("absent", record["actual"])
        for path, blob in result["unchanged_oracles"].items():
            self.assertEqual(blob, self.git("hash-object", str(self.tree / path)))
        self.assertEqual(1, len(result["adapted_fixtures"]))
        for record in result["adapted_fixtures"]:
            path = record["path"]
            self.assertEqual(self.git("rev-parse", self.candidate + ":" + path), record["candidate_blob"])
            self.assertEqual(HELPER.ADAPTED_FIXTURES[path], record["baseline_blob"])
            self.assertEqual(record["baseline_blob"], self.git("hash-object", str(self.tree / path)))
            self.assertEqual(record["baseline_blob"], record["actual_blob"])

    def test_adapted_fixture_wrong_baseline_blob_rejected_before_mutation(self):
        self.make_repository()
        path = next(iter(HELPER.ADAPTED_FIXTURES))
        before = (self.tree / path).read_bytes()
        with mock.patch.object(HELPER, "ADAPTED_FIXTURES", {path: "0" * 40}):
            with self.assertRaisesRegex(ValueError, "adapted fixture baseline blob"):
                self.run_helper()
        self.assertEqual(before, (self.tree / path).read_bytes())
        self.assertTrue(all((self.tree / fixture).exists() for fixture in HELPER.FIXTURES))
        self.assertFalse(self.manifest.exists())

    def test_dirty_last_fixture_is_rejected_before_any_removal(self):
        self.make_repository()
        self.write(HELPER.FIXTURES[-1], "dirty final candidate fixture\n")
        with self.assertRaisesRegex(ValueError, "dirty candidate"):
            self.run_helper()
        self.assertTrue(all((self.tree / path).exists() for path in HELPER.FIXTURES))
        self.assertFalse(self.manifest.exists())

    def test_external_git_environment_cannot_redirect_helper_or_mutate_foreign_repo(self):
        self.make_repository()
        foreign = self.tree.with_name("foreign")
        foreign.mkdir()

        def foreign_git(*args):
            return subprocess.run(
                ["git", "-C", str(foreign), "-c", "user.name=CI fixture", "-c", "user.email=fixture@example.invalid",
                 "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + os.devnull, "-c", "init.templateDir=", *args],
                env=self.env, check=True, capture_output=True, text=True, timeout=15,
            ).stdout.strip()

        foreign_git("init", "-q")
        working = foreign / "keep.txt"
        working.write_text("foreign working data\n")
        foreign_git("add", ".")
        foreign_git("commit", "-qm", "foreign state")
        index = foreign / ".git/index"
        before = (foreign_git("rev-parse", "HEAD"), index.read_bytes(), working.read_bytes())
        poison = {"GIT_DIR": str(foreign / ".git"), "GIT_WORK_TREE": str(foreign), "GIT_INDEX_FILE": str(index)}
        with mock.patch.dict(os.environ, poison):
            result = self.run_helper()
            self.assertTrue(all(os.environ[key] == value for key, value in poison.items()))
        self.assertEqual(self.candidate, result["candidate"])
        self.assertEqual(12, len(result["fixtures"]))
        self.assertEqual(before, (foreign_git("rev-parse", "HEAD"), index.read_bytes(), working.read_bytes()))

    def test_preexisting_baseline_fixture_is_rejected(self):
        self.make_repository(baseline_fixture=True)
        with self.assertRaisesRegex(ValueError, "candidate-only"):
            self.run_helper()
        self.assertTrue(all((self.tree / path).exists() for path in HELPER.FIXTURES))

    def test_preexisting_baseline_contract_is_rejected(self):
        self.make_repository(baseline_contract=True)
        with self.assertRaisesRegex(ValueError, "contract must be absent"):
            self.run_helper()
        self.assertFalse(self.manifest.exists())

    def test_dirty_unsupported_and_symlink_states_fail_closed(self):
        scenarios = (
            "unsupported-baseline", "candidate-mismatch", "candidate-not-sha", "dirty-production", "dirty-helper",
            "missing-helper", "missing-fixture", "fixture-directory", "fixture-symlink", "parent-symlink",
            "helper-symlink", "root-symlink", "unexpected-contract", "unexpected-go-file", "ignored-go-file",
            "dirty-ticket", "manifest-existing", "manifest-symlink", "manifest-parent-symlink",
            "dirty-adapted", "missing-adapted", "adapted-symlink",
        )
        for scenario in scenarios:
            with self.subTest(scenario=scenario):
                self.make_repository()
                baseline, candidate = self.baseline, self.candidate
                path = self.tree / HELPER.FIXTURES[-1]
                if scenario == "unsupported-baseline":
                    baseline = "0" * 40
                elif scenario == "candidate-mismatch":
                    candidate = self.baseline
                elif scenario == "candidate-not-sha":
                    candidate = "HEAD"
                elif scenario == "dirty-production":
                    self.write(self.production, "unexpected production change\n")
                elif scenario == "dirty-helper":
                    self.write(HELPER.REDIS_HELPER, "unexpected helper change\n")
                elif scenario == "missing-helper":
                    (self.tree / HELPER.REDIS_HELPER).unlink()
                elif scenario == "missing-fixture":
                    path.unlink()
                elif scenario == "fixture-directory":
                    path.unlink()
                    path.mkdir()
                elif scenario == "fixture-symlink":
                    path.unlink()
                    path.symlink_to(self.tree / self.production)
                elif scenario == "parent-symlink":
                    directory = self.tree / "backend/internal/service"
                    directory.rename(directory.with_name("real-service"))
                    directory.symlink_to(directory.with_name("real-service"), target_is_directory=True)
                elif scenario == "helper-symlink":
                    helper = self.tree / HELPER.REDIS_HELPER
                    helper.unlink()
                    helper.symlink_to(self.tree / self.production)
                elif scenario == "root-symlink":
                    alias = self.tree.with_name("tree-alias")
                    alias.symlink_to(self.tree, target_is_directory=True)
                    self.tree = alias
                elif scenario == "unexpected-contract":
                    self.write(HELPER.CONTRACT, "unexpected contract restoration\n")
                elif scenario == "unexpected-go-file":
                    self.write("backend/internal/service/unexpected.go", "package service\n")
                elif scenario == "ignored-go-file":
                    self.write("backend/internal/service/unexpected.go", "package service\n")
                    self.write(".git/info/exclude", "unexpected.go\n")
                elif scenario == "dirty-ticket":
                    self.write(HELPER.ORACLES[0], "unexpected ticket oracle mutation\n")
                elif scenario == "dirty-adapted":
                    self.write(next(iter(HELPER.ADAPTED_FIXTURES)), "dirty adapted fixture\n")
                elif scenario == "missing-adapted":
                    (self.tree / next(iter(HELPER.ADAPTED_FIXTURES))).unlink()
                elif scenario == "adapted-symlink":
                    adapted = self.tree / next(iter(HELPER.ADAPTED_FIXTURES))
                    adapted.unlink()
                    adapted.symlink_to(self.tree / self.production)
                elif scenario == "manifest-existing":
                    self.manifest.write_text("existing evidence\n")
                elif scenario == "manifest-symlink":
                    self.manifest.symlink_to(self.tree / self.production)
                elif scenario == "manifest-parent-symlink":
                    directory = self.manifest.parent
                    directory.rename(directory.with_name("real-evidence"))
                    directory.symlink_to(directory.with_name("real-evidence"), target_is_directory=True)
                with self.assertRaises((ValueError, subprocess.SubprocessError)):
                    self.run_helper(baseline, candidate)
                self.assertTrue((self.tree / HELPER.FIXTURES[0]).is_file(), "reject before mutating first fixture")
                if scenario not in {"manifest-existing", "manifest-symlink"}:
                    self.assertFalse(self.manifest.exists())


if __name__ == "__main__":
    unittest.main()
