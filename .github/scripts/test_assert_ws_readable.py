"""Run only on Actions; validate clean per-leaf WS RED and exact GREEN."""

import importlib.util
import json
import os
import re
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("gate", Path(__file__).with_name("assert-ws-readable.py"))
GATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GATE)


def evidence(phase, suite):
    root, names = GATE.SUITES[suite]
    terminal = "fail" if phase == "RED" else "pass"
    events = [{"Action": "run", "Test": root}]
    for suffix in names:
        name = f"{root}/{suffix}"
        events.append({"Action": "run", "Test": name})
        if phase == "RED":
            events.append({"Action": "output", "Test": name, "Output": " ".join(GATE.diagnostic(suite, name))})
        events.append({"Action": terminal, "Test": name})
    events.extend([{"Action": terminal, "Test": root}, {"Action": terminal}])
    for event in events:
        event["Package"] = GATE.COMMON.PACKAGE.replace("/handler", "/securityaudit") if suite in {"audit-persistence", "audit-async"} else GATE.COMMON.PACKAGE
        if suite == "ws-ticket":
            event["Package"] = GATE.COMMON.PACKAGE.replace("/handler", "/service")
    return events


def raw(events):
    return "".join(json.dumps(event) + "\n" for event in events)


class ResidualFixtureOwnershipTest(unittest.TestCase):
    repository = Path(__file__).resolve().parents[2]

    def residual_step(self):
        workflow = (self.repository / ".github/workflows/ws-readable-continuation.yml").read_text()
        return workflow.split("      - name: Residual headers tools and ticket actual transport counterexamples\n", 1)[1].split("      - name:", 1)[0]

    def inline_loop(self):
        match = re.search(r"(?ms)^            for target in (backend/.*?); do\n.*?^            done$", self.residual_step())
        self.assertIsNotNone(match)
        return textwrap.dedent(match.group()), match.group(1).split()

    def test_actual_residual_rollback_and_exact_test_support_overlay(self):
        step = self.residual_step()
        rollback = re.search(r"(?ms)^            while IFS= read -r target; do\n.*?^            git -C .*?diff --exit-code .*?':!backend/\*\*/\*_test.go'$", step)
        self.assertIsNotNone(rollback)
        overlay = re.search(r"(?ms)^            # BEGIN residual Redis test-support overlay\n.*?^            # END residual Redis test-support overlay$", step)
        self.assertIsNotNone(overlay)
        self.assertLess(rollback.end(), overlay.start())
        self.assertEqual(step[:overlay.start()].count("':!backend/**/*_test.go'"), 1)
        rollback = textwrap.dedent(rollback.group())
        overlay = textwrap.dedent(overlay.group())
        helper = "backend/internal/testutil/redissession/store.go"
        production = "backend/internal/service/production.go"
        unrelated = "backend/internal/service/new_production.go"
        oauth = "backend/internal/service/openai_oauth_reauth_request_proxy_test.go"
        selected = {"backend/internal/handler/openai_409_ws_rebuild_test.go", "backend/internal/service/openai_409_ws_ticket_test.go"}
        fixture_sources = {path: "candidate selected test\n" for path in selected}
        fixture_sources[oauth] = 'package service\nimport _ "github.com/Wei-Shaw/sub2api/internal/testutil/redissession"\n'
        for scenario in ("valid", "dirty-source", "other-path", "other-production", "source-symlink", "unexpected-target"):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as directory:
                source, red = Path(directory) / "candidate", Path(directory) / "red"
                source.mkdir()
                env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
                env.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull)

                def git(*args, cwd=source):
                    return subprocess.run(["git", "-c", "user.name=CI fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + os.devnull, "-c", "init.templateDir=", *args], cwd=cwd, env=env, check=True, capture_output=True, text=True, timeout=15).stdout.strip()

                def write(root, path, content):
                    target = root / path
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_text(content)

                def run(shell):
                    return subprocess.run(["bash", "-c", "set -euo pipefail\n" + shell], cwd=source, env=env, capture_output=True, text=True, timeout=15)

                git("init", "-q")
                write(source, production, "package service // baseline\n")
                for path in selected:
                    write(source, path, "baseline selected test\n")
                git("add", ".")
                git("commit", "-qm", "baseline")
                baseline = git("rev-parse", "HEAD")
                write(source, production, "package service // candidate\n")
                write(source, unrelated, "package service // candidate-only production\n")
                write(source, helper, "package redissession // candidate test support\n")
                for path, content in fixture_sources.items():
                    write(source, path, content)
                git("add", ".")
                git("commit", "-qm", "candidate")
                candidate = git("rev-parse", "HEAD")
                helper_blob = git("rev-parse", f"{candidate}:{helper}")
                git("worktree", "add", "--detach", str(red), candidate)
                evidence_dir = Path(directory) / "evidence/ws-evidence"
                evidence_dir.mkdir(parents=True)
                env.update(baseline=baseline, CANDIDATE_SHA=candidate, tested_tree=str(red), GITHUB_WORKSPACE=str(source), RUNNER_TEMP=str(evidence_dir.parent))

                result = run(rollback)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertFalse((red / helper).exists(), "the actual old rollback must delete the new non-test helper")
                self.assertFalse((red / unrelated).exists())
                self.assertIn("internal/testutil/redissession", (red / oauth).read_text(), "the importing candidate test survives the actual old rollback")
                self.assertEqual((red / production).read_text(), "package service // baseline\n")
                self.assertEqual(git("diff", "--name-only", baseline, "--", "backend/**/*.go", ":!backend/**/*_test.go", cwd=red), "")
                script = overlay
                if scenario == "dirty-source":
                    write(source, helper, "unexpected candidate helper mutation\n")
                elif scenario == "other-path":
                    script = script.replace("helper=" + helper, "helper=" + unrelated, 1)
                elif scenario == "other-production":
                    write(red, production, "unexpected production mutation\n")
                elif scenario == "source-symlink":
                    (source / helper).unlink()
                    (source / helper).symlink_to(source / production)
                elif scenario == "unexpected-target":
                    write(red, helper, "unexpected restored helper\n")
                result = run(script)
                manifest = evidence_dir / "residual-test-support.txt"
                if scenario != "valid":
                    self.assertNotEqual(result.returncode, 0, "untrusted helper state or another production path must be rejected")
                    self.assertFalse(manifest.exists(), "rejected preparation must not produce a success manifest")
                    continue
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(git("hash-object", str(red / helper)), helper_blob)
                self.assertEqual(git("diff", "--name-only", baseline, "--", "backend/**/*.go", ":!backend/**/*_test.go", cwd=red), helper)
                self.assertEqual((red / production).read_text(), "package service // baseline\n")
                self.assertFalse((red / unrelated).exists())
                for path, content in fixture_sources.items():
                    self.assertEqual((red / path).read_text(), content)
                fields = dict(field.split("=", 1) for field in manifest.read_text().split())
                self.assertEqual(fields, {"path": helper, "baseline": baseline, "baseline_blob": "absent", "candidate": candidate, "candidate_blob": helper_blob, "source_blob": helper_blob, "restored_blob": helper_blob})

    def test_residual_fixtures_have_one_owner_in_shared_helper_chain(self):
        _, inline = self.inline_loop()
        self.assertEqual(len(inline), len(set(inline)))
        pending = re.findall(r"\.github/scripts/([\w.-]+\.sh)", self.residual_step())
        visited, shared = set(), set()
        while pending:
            name = pending.pop()
            if name in visited:
                continue
            visited.add(name)
            source = (self.repository / ".github/scripts" / name).read_text()
            pending.extend(re.findall(r"\$script_dir/([\w.-]+\.sh)", source))
            for pattern in re.findall(r"backend/[\w./*?-]+_test\.go", source):
                shared.add(pattern)
                shared.update(str(path.relative_to(self.repository)) for path in self.repository.glob(pattern))
        self.assertGreaterEqual(len(visited), 7)
        self.assertFalse(set(inline) & shared, "a strict candidate-blob check cannot follow another owner's overlay")
        self.assertIn("backend/internal/service/openai_excel_bps_search_business_test.go", inline)
        self.assertIn("backend/internal/service/openai_excel_bps_search_test.go", shared)
        for selected in ("backend/internal/handler/openai_409_ws_rebuild_test.go", "backend/internal/service/openai_409_ws_ticket_test.go"):
            self.assertNotIn(selected, inline)

    def test_actual_shell_handoff_preserves_guards_and_rejects_duplicate_owner(self):
        loop, inline = self.inline_loop()
        replay = (self.repository / ".github/scripts/prepare-replay-red-fixtures.sh").read_text()
        exclude = re.search(r"(?ms)^exclude_absent_fixture\(\) \{\n.*?^\}", replay).group()
        group = exclude + "\ncontract=backend/internal/service/group.go\n" + replay.split("contract=backend/internal/service/group.go\n", 1)[1].split("\n# Older Account/search", 1)[0]
        shared = re.search(r"for target in (.*?); do", group).group(1).split()
        self.assertEqual(len(shared), 4)
        duplicate = "backend/internal/service/admin_group_duplicate_test.go"
        business = "backend/internal/service/openai_excel_bps_search_business_test.go"
        absent = {business, "backend/internal/service/free_ultrafast_customer_cost_test.go"}
        selected = {"backend/internal/handler/openai_409_ws_rebuild_test.go", "backend/internal/service/openai_409_ws_ticket_test.go"}
        for scenario in ("valid", "duplicate", "dirty"):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as directory:
                tree = Path(directory)
                env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
                env.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull)

                def git(*args):
                    return subprocess.run(["git", "-c", "user.name=CI fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + os.devnull, "-c", "init.templateDir=", *args], cwd=tree, env=env, check=True, capture_output=True, text=True, timeout=15).stdout.strip()

                def write(path, content):
                    target = tree / path
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_text(content)

                git("init", "-q")
                contract = "backend/internal/service/group.go"
                write(contract, "package service\n")
                for path in set(inline + shared) | selected:
                    if path not in absent:
                        write(path, "baseline\n")
                git("add", ".")
                git("commit", "-qm", "baseline")
                baseline = git("rev-parse", "HEAD")
                for path in set(inline + shared) | selected:
                    write(path, "candidate\n")
                git("add", ".")
                git("commit", "-qm", "candidate")
                candidate = git("rev-parse", "HEAD")
                evidence = tree / "evidence/ws-evidence"
                evidence.mkdir(parents=True)
                env.update(baseline=baseline, candidate=candidate, tree=str(tree), BASELINE_SHA=baseline, CANDIDATE_SHA=candidate, RUNNER_TEMP=str(evidence.parent), tested_tree=str(tree))
                if scenario == "dirty":
                    write(duplicate, "unexpected fixture mutation\n")
                prepared = subprocess.run(["bash", "-c", "set -euo pipefail\n" + group], cwd=tree, env=env, capture_output=True, text=True, timeout=15)
                if scenario == "dirty":
                    self.assertNotEqual(prepared.returncode, 0, "candidate-blob checks must remain strict")
                    continue
                self.assertEqual(prepared.returncode, 0, prepared.stderr)
                script = loop if scenario == "valid" else loop.replace("; do", " " + duplicate + "; do", 1)
                result = subprocess.run(["bash", "-c", "set -euo pipefail\n" + script], cwd=tree, env=env, capture_output=True, text=True, timeout=15)
                if scenario == "duplicate":
                    self.assertNotEqual(result.returncode, 0, "the former double-owner sequence must reproduce its failure")
                    continue
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(len((evidence / "residual-service-fixtures.txt").read_text().splitlines()), len(inline))
                for path in set(inline + shared):
                    if path in absent:
                        self.assertFalse((tree / path).exists())
                    else:
                        self.assertEqual((tree / path).read_text(), "baseline\n")
                for path in selected:
                    self.assertEqual((tree / path).read_text(), "candidate\n")
                self.assertEqual((tree / contract).read_text(), "package service\n")


class WSReadableEvidenceTest(unittest.TestCase):
    def test_recovered_audit_red_requires_actual_restored_B_completion(self):
        suite = "ws-recovered-audit"
        events = evidence("RED", suite)
        GATE.validate("RED", 1, raw(events), suite)
        for event in events:
            if event["Action"] != "output":
                continue
            original = event["Output"]
            event["Output"] = "WS_RECOVERED_AUDIT_DISPATCHED: unrelated client close"
            with self.assertRaises(ValueError):
                GATE.validate("RED", 1, raw(events), suite)
            event["Output"] = original

    def strict_baseline(self, failures):
        events = evidence("GREEN", "strict-pointer")
        root, _ = GATE.SUITES["strict-pointer"]
        names = {f"{root}/{suffix}" for suffix in failures}
        for event in events:
            if event["Action"] == "pass" and (event.get("Test") in names or (failures and event.get("Test") in {root, None})):
                event["Action"] = "fail"
        for name in names:
            events.append({"Package": GATE.COMMON.PACKAGE, "Action": "output", "Test": name, "Output": "HTTP_POINTER_STRICT_A_STATE_LEAK"})
        return events

    def test_strict_baseline_pins_each_expected_leaf(self):
        names = GATE.SUITES["strict-pointer"][1]
        failures = {"apikey/stream=false/strict", "apikey/stream=true/strict"}
        events = self.strict_baseline(failures)
        GATE.validate("BASELINE", 1, raw(events), "strict-pointer")
        GATE.validate("GREEN", 0, raw(evidence("GREEN", "strict-pointer")), "strict-pointer")
        for incorrect in (set(), names, names - failures, {"apikey/stream=false/strict", "oauth/stream=true/strict"}):
            with self.assertRaises(ValueError):
                GATE.validate("BASELINE", int(bool(incorrect)), raw(self.strict_baseline(incorrect)), "strict-pointer")
        for code in (0, 1):
            with self.assertRaises(ValueError):
                GATE.validate("GREEN", code, raw(events), "strict-pointer")
        with self.assertRaises(ValueError):
            GATE.validate("RED", 1, raw(self.strict_baseline(names)), "strict-pointer")
        for index, original in enumerate(events):
            if original["Action"] in {"run", "pass", "fail", "output"}:
                with self.assertRaises(ValueError):
                    GATE.validate("BASELINE", 1, raw(events[:index] + events[index + 1:]), "strict-pointer")
            if original["Action"] in {"run", "pass", "fail"}:
                with self.assertRaises(ValueError):
                    GATE.validate("BASELINE", 1, raw(events + [original]), "strict-pointer")
            if original["Action"] in {"pass", "fail"}:
                wrong = dict(original, Action="pass" if original["Action"] == "fail" else "fail")
                with self.assertRaises(ValueError):
                    GATE.validate("BASELINE", 1, raw(events[:index] + [wrong] + events[index + 1:]), "strict-pointer")
        for wrong_exit in (0, 2, 137):
            with self.assertRaises(ValueError):
                GATE.validate("BASELINE", wrong_exit, raw(events), "strict-pointer")
        for poison in ("panic: fixture", "WARNING: DATA RACE", "openai.usage_record_task_panic_recovered", "fatal error: crash", "signal: killed", "undefined: fixture", "[no tests to run]", "--- SKIP: fixture"):
            for contamination in (poison, raw([{"Action": "output", "Output": poison}])):
                with self.assertRaises(ValueError):
                    GATE.validate("BASELINE", 1, raw(events) + contamination, "strict-pointer")
        for action in ("run", "pass", "fail", "skip"):
            with self.assertRaises(ValueError):
                GATE.validate("BASELINE", 1, raw(events + [{"Package": GATE.COMMON.PACKAGE, "Action": action, "Test": "Unrelated"}]), "strict-pointer")
        with self.assertRaises(ValueError):
            GATE.validate("BASELINE", 1, raw(events + [{"Package": "unrelated/package", "Action": "pass"}]), "strict-pointer")

    def test_valid(self):
        GATE.validate("RED", 1, raw(evidence("RED", "ws")), "ws")
        for suite in GATE.SUITES:
            GATE.validate("GREEN", 0, raw(evidence("GREEN", suite)), suite)
        for suite in GATE.RED_SUITES:
            GATE.validate("RED", 1, raw(evidence("RED", suite)), suite)

    def test_repair_red_needs_each_own_business_diagnostic(self):
        for suite in GATE.RED_SUITES:
            for index, original in enumerate(evidence("RED", suite)):
                if original.get("Action") != "output":
                    continue
                events = evidence("RED", suite)
                events[index]["Output"] = "unrelated error"
                with self.assertRaises(ValueError):
                    GATE.validate("RED", 1, raw(events), suite)

    def test_tool_red_requires_actual_transport_message_on_both_leaves(self):
        # Keep this independent of diagnostic(): the actual baseline emits
        # ClientMessage(), not its internal wire_changed reason identifier.
        message = "The proven encrypted agent transport changed."
        marker = "WS_RESIDUAL_TOOL_PROJECTION_REFUSED"
        events = evidence("RED", "ws-residual")
        for event in events:
            if event.get("Action") == "output" and "/tools_" in event.get("Test", ""):
                event["Output"] = f"{marker}: {message} Preserve this task and context."
        GATE.validate("RED", 1, raw(events), "ws-residual")
        for leaf in ("tools_omitted", "tools_explicit"):
            for wrong in (marker, message, f"{marker}: wire_changed", f"{marker}: upstream failed"):
                changed = [dict(event) for event in events]
                for event in changed:
                    if event.get("Action") == "output" and event.get("Test", "").endswith("/" + leaf):
                        event["Output"] = wrong
                with self.assertRaises(ValueError):
                    GATE.validate("RED", 1, raw(changed), "ws-residual")
        for poison in ("undefined: fixture", "panic: fixture", "WARNING: DATA RACE", "--- SKIP: fixture"):
            with self.assertRaises(ValueError):
                GATE.validate("RED", 1, raw(events) + poison, "ws-residual")

    def test_each_leaf_requires_both_diagnostics(self):
        for suffix in GATE.SUITES["ws"][1]:
            for diagnostic in ("WS_READABLE_B_DISPATCH_MISSING", "account eligibility changed", "other"):
                events = evidence("RED", "ws")
                for event in events:
                    if event.get("Test", "").endswith("/" + suffix) and event["Action"] == "output":
                        event["Output"] = diagnostic
                with self.assertRaises(ValueError):
                    GATE.validate("RED", 1, raw(events), "ws")

    def test_each_green_leaf_required(self):
        for suite, (root, names) in GATE.SUITES.items():
            for suffix in names:
                events = [e for e in evidence("GREEN", suite) if e.get("Test") != f"{root}/{suffix}"]
                with self.assertRaises(ValueError):
                    GATE.validate("GREEN", 0, raw(events), suite)

    def test_contamination(self):
        for phase, code in (("RED", 1), ("GREEN", 0)):
            for poison in ("panic: fixture", "WARNING: DATA RACE", "openai.usage_record_task_panic_recovered", "fatal error: crash", "signal: killed", "undefined: fixture", "[no tests to run]", "--- SKIP: fixture"):
                for encoded in (False, True):
                    contamination = raw([{"Action": "output", "Output": poison}]) if encoded else poison
                    with self.assertRaises(ValueError):
                        GATE.validate(phase, code, raw(evidence(phase, "ws")) + contamination, "ws")


if __name__ == "__main__":
    unittest.main()
