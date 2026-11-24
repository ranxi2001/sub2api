#!/usr/bin/env python3
"""CI-only, named PR1050 follow-up evidence; supplements the v0222 gates."""
import itertools
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
PREFIX = "TestOpenAIGatewayService_APIKeyPassthrough_"
SECRET = PREFIX + "HeaderOverrideSecrets"
PROTOCOL = PREFIX + "ProtocolCodeWhitespaceAndSecretCollision"
QUERY = "TestUpstreamBillingGroupQueryPreservesExistingParameters"
GUARD = "TestUpdateAccountUpstreamGroupGuardPreservesConcurrentEdits"
ATOMIC = "TestUpdateAccountUpstreamGroupGuardRequiresAtomicRepository"
HANDLER = "TestAccountHandlerUpstreamGroupPreviewBindsGuardAndUpdatePassesIt"
PG = "TestAccountUpstreamGroupSyncVersionProtectsReadWriteBoundary"
LEAVES = {
    SECRET: [f"{SECRET}/{branch}/{mode}/{case}" for branch, mode, case in itertools.product(
        ["default", "rule_body", "rule_message"], ["json", "compact_committed"],
        ["static", "identity_template", "overlapping_literals", "short_control", "separator_control", "identity_only_control"])],
    PROTOCOL: [f"{PROTOCOL}/{code}/nested={nested}/secret={secret}/compact={compact}"
               for code, nested, secret, compact in itertools.product(
                   ["invalid_encrypted_content", "thinking_signature_invalid"], *([["false", "true"]] * 3))],
    QUERY: [f"{QUERY}/{mode}/{suffix}" for mode, suffix in itertools.product(
        ["preview", "scheduled_group", "scheduled_rate_only"],
        ["", "?tenant=demo&auth=synthetic%2Btoken&tag=a&tag=b", "?tenant=demo&auth=synthetic%2Btoken&include=previous#display-only"])],
    GUARD: [f"{GUARD}/{case}" for case in ["none", "name", "notes", "both", "priority", "api_key", "base_url", "api_base_urls", "header_overrides", "proxy", "platform"]],
    ATOMIC: [], HANDLER: [],
    PG: [f"{PG}/{case}" for case in ["none", "name", "notes", "api_key", "base_url", "priority"]],
    PREFIX + "NestedErrorCodePreservesProtocolAndSanitization": [],
    PREFIX + "StructuredErrorHardening": [],
}
VIEW = "src/views/admin/__tests__/AccountsView.lite.spec.ts"
API = "src/api/__tests__/admin.accounts.upstreamBillingProbe.spec.ts"
SPECS = [VIEW, API,
         "src/views/admin/__tests__/AccountsView.sparkShadow.spec.ts",
         "src/components/account/__tests__/HeaderOverrideEditor.spec.ts",
         "src/components/account/__tests__/CreateAccountModal.spec.ts",
         "src/components/account/__tests__/EditAccountModal.spec.ts",
         "src/components/account/__tests__/AccountUpstreamErrorNotice.spec.ts",
         "src/components/common/__tests__/NewFeatureBadge.spec.ts",
         "src/stores/__tests__/newFeatureNotices.spec.ts"]
REQUIRED_TITLES = {
    VIEW: [f"protects a completed '{label}' edit from a delayed group preview" for label in
           ["name", "notes", "both text fields", "credentials", "URL", "protocol URL", "proxy", "platform", "unrelated priority", "unchanged"]]
          + [f"keeps local text on {scenario}" for scenario in ["preview failure", "patch conflict", "missing guard", "unmounted"]],
    API: ["maps upstream group text to editable fields and skips empty values"],
    "src/views/admin/__tests__/AccountsView.sparkShadow.spec.ts": [
        "仅将具有安全 base_url 的 API Key 账号名称链接到站点主页"],
    "src/components/account/__tests__/HeaderOverrideEditor.spec.ts": [
        "adds a suggested row when no value input is being edited",
        "leaves the name empty when the suggested name is taken by another value",
        "inserts at the caret of the value input being edited",
        "shows a preview for valid variables and an inline error for unknown ones",
        "hides the variable picker and flags variables when the account cannot report identity"],
    "src/components/account/__tests__/CreateAccountModal.spec.ts": [
        "fills the name and notes from the unsaved API key and its adaptive Chat URL",
        "creates an API key account with group sync only after opt-in"],
    "src/components/account/__tests__/EditAccountModal.spec.ts": [
        "opts into group sync independently and clears it when probing stops"],
    "src/components/common/__tests__/NewFeatureBadge.spec.ts": [
        "hides a shared notice for the current admin when clicked"],
    "src/stores/__tests__/newFeatureNotices.spec.ts": [
        "persists dismissed notice IDs per admin account"],
}


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n")


def command(argv, cwd, stem, evidence):
    record = {"argv": argv, "cwd": str(cwd), "stdout": stem + ".stdout", "stderr": stem + ".stderr"}
    with (evidence / record["stdout"]).open("w") as stdout, (evidence / record["stderr"]).open("w") as stderr:
        try:
            result = subprocess.run(argv, cwd=cwd, stdout=stdout, stderr=stderr, timeout=1200, check=False)
            record["exit_code"] = result.returncode
        except subprocess.TimeoutExpired:
            record.update(exit_code=None, timed_out=True)
    write_json(evidence / (stem + "-command.json"), record)
    print(f"{stem}: exit={record['exit_code']}", flush=True)
    return record


def verify_go(evidence, stem, record, names, package):
    events = [json.loads(line) for line in (evidence / record["stdout"]).read_text().splitlines() if line.strip()]
    expected_package = "github.com/Wei-Shaw/sub2api/" + package
    runs, passes = set(), set()
    failures = []
    package_pass = False
    for event in events:
        action, name = event.get("Action"), event.get("Test")
        if action in ("fail", "skip", "build-fail"):
            failures.append(event)
        if name:
            if event.get("Package") != expected_package:
                raise RuntimeError(f"Unexpected Go package: {event}")
            if action == "run":
                runs.add(name)
            if action == "pass":
                passes.add(name)
        elif action == "pass" and event.get("Package") == expected_package:
            package_pass = True
    required = set(names)
    for name in names:
        required.update(LEAVES[name])
    missing = sorted(required - (runs & passes))
    # Every actually started subtest must finish successfully, including intermediate parents.
    incomplete = sorted(runs - passes)
    result = {"required": sorted(required), "ran": sorted(runs), "passed": sorted(passes),
              "missing": missing, "incomplete": incomplete, "failures": failures, "package_pass": package_pass,
              "exit_code": record["exit_code"]}
    write_json(evidence / (stem + "-verification.json"), result)
    if record["exit_code"] != 0 or missing or incomplete or failures or not package_pass:
        raise RuntimeError(f"{stem}: failed execution/missing/skip/compile gate; see raw logs and verification")
    return result


def verify_frontend(evidence, record):
    report = json.loads((evidence / "frontend.json").read_text())
    if record["exit_code"] != 0 or report.get("success") is not True:
        raise RuntimeError("Vitest command/report failed")
    for key in ("numFailedTests", "numPendingTests", "numTodoTests", "numFailedTestSuites", "numPendingTestSuites"):
        if report.get(key, 0) != 0:
            raise RuntimeError(f"Vitest {key}: {report[key]}")
    suites = {Path(suite["name"]).resolve(): suite for suite in report["testResults"]}
    if set(suites) != {(ROOT / "frontend" / spec).resolve() for spec in SPECS}:
        raise RuntimeError("Missing or unexpected frontend test paths")
    verified = {}
    for spec in SPECS:
        suite = suites[(ROOT / "frontend" / spec).resolve()]
        assertions = suite["assertionResults"]
        if suite["status"] != "passed" or not assertions or any(a["status"] != "passed" for a in assertions):
            raise RuntimeError(f"Empty, skipped or failed suite: {spec}")
        titles = [a["title"] for a in assertions]
        missing = set(REQUIRED_TITLES.get(spec, [])) - set(titles)
        if missing:
            raise RuntimeError(f"Missing named regressions in {spec}: {sorted(missing)}")
        verified[spec] = [a["fullName"] for a in assertions]
    write_json(evidence / "frontend-verification.json", verified)
    return verified


def main():
    if os.environ.get("CI") != "true":
        raise RuntimeError("Run only in CI with isolated services")
    head = os.environ["EXPECTED_HEAD_SHA"]
    evidence = Path(os.environ["EVIDENCE_DIR"]).resolve()
    evidence.relative_to(Path(os.environ["RUNNER_TEMP"]).resolve())
    evidence.mkdir(parents=True, exist_ok=True)
    report = {"expected_head": head, "head": git("rev-parse", "HEAD"), "tree": git("rev-parse", "HEAD^{tree}"),
              "scope": "PR1050 follow-up regressions only; existing v0222 acceptance remains independent", "results": {}, "errors": []}
    try:
        if not re.fullmatch(r"[0-9a-f]{40}", head) or report["head"] != head or git("status", "--porcelain"):
            raise RuntimeError("Expected exact clean candidate HEAD")
        jobs = [("service", "unit", "internal/service", [SECRET, PROTOCOL, QUERY, GUARD, ATOMIC,
                  PREFIX + "NestedErrorCodePreservesProtocolAndSanitization", PREFIX + "StructuredErrorHardening"]),
                ("handler", "unit", "internal/handler/admin", [HANDLER]),
                ("postgres", "integration", "internal/repository", [PG])]
        for stem, tag, package, names in jobs:
            try:
                argv = ["go", "test", "-race", "-json", f"-tags={tag}", "./" + package,
                        "-run", "^(" + "|".join(names) + ")$", "-count=1", "-timeout=15m"]
                record = command(argv, ROOT / "backend", stem, evidence)
                report["results"][stem] = verify_go(evidence, stem, record, names, package)
            except Exception as error:
                report["errors"].append(f"{stem}: {error}")
        try:
            for spec in SPECS:
                if not (ROOT / "frontend" / spec).is_file():
                    raise RuntimeError(f"Missing frontend source: {spec}")
            argv = ["pnpm", "exec", "vitest", "run", *SPECS, "--maxWorkers=1", "--minWorkers=1", "--no-file-parallelism",
                    "--reporter=default", "--reporter=json", "--outputFile=" + str(evidence / "frontend.json")]
            record = command(argv, ROOT / "frontend", "frontend", evidence)
            report["results"]["frontend"] = verify_frontend(evidence, record)
        except Exception as error:
            report["errors"].append(f"frontend: {error}")
        if git("status", "--porcelain") or git("rev-parse", "HEAD") != head:
            raise RuntimeError("Source changed while collecting evidence")
    except Exception as error:
        report["errors"].append(str(error))
    finally:
        report["passed"] = not report["errors"] and len(report["results"]) == 4
        write_json(evidence / "manifest.json", report)
    if not report["passed"]:
        sys.exit("\n".join(report["errors"]) or "Incomplete acceptance")


if __name__ == "__main__":
    main()
