import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import yaml

SPEC = importlib.util.spec_from_file_location('ci_select', Path(__file__).with_name('select_jobs.py'))
ci = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ci)
REPO = ci.ROOT


class SelectionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.patch = patch.object(ci, 'ROOT', self.root)
        self.patch.start()
        self.addCleanup(self.patch.stop)
        self.env = patch.dict('os.environ', {'GITHUB_REF': 'refs/pull/7/merge'})
        self.env.start()
        self.addCleanup(self.env.stop)
        ci.tree.cache_clear()
        ci.workflow_at.cache_clear()
        self.git('init', '-q')
        self.git('config', 'user.email', 'fixture@example.invalid')
        self.git('config', 'user.name', 'Fixture')
        self.write('.github/workflows/main.yml', 'name: CI\npermissions:\n  contents: read\njobs:\n  backend:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v6\n        with:\n          ref: ${{ github.event.pull_request.head.sha || github.sha }}\n      - run: go test ./...\n  frontend:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v6\n        with:\n          ref: ${{ github.event.pull_request.head.sha || github.sha }}\n      - run: pnpm test\n')
        self.write('backend/a.go', 'package a\n')
        self.write('frontend/a.ts', 'export const a = 1\n')
        self.base = self.save()
        self.scopes = {'backend': ['backend/**'], 'frontend': ['frontend/**']}

    def git(self, *args):
        return subprocess.check_output(['git', *args], cwd=self.root, stderr=subprocess.DEVNULL).decode().strip()

    def write(self, path, text):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)

    def save(self):
        self.git('add', '-A')
        self.git('commit', '-qm', 'fixture')
        return self.git('rev-parse', 'HEAD')

    def event(self):
        return {'pull_request': {'number': 7, 'base': {'sha': self.base}, 'head': {'ref': 'feature', 'repo': {'full_name': 'fixture/repo'}}}}

    def select(self, head, evidence=None):
        return ci.select('main.yml', self.scopes, 'pull_request', self.event(), head, evidence)

    def test_docs_and_frontend_changes_do_not_run_backend(self):
        self.write('docs/guide.md', 'docs')
        docs = self.save()
        self.assertFalse(any(v[0] for v in self.select(docs).values()))
        self.write('frontend/a.ts', 'changed')
        result = self.select(self.save())
        self.assertFalse(result['backend'][0])
        self.assertTrue(result['frontend'][0])

    def test_deletions_renames_and_dependency_changes_are_affected(self):
        (self.root / 'backend/a.go').rename(self.root / 'backend/b.go')
        self.assertTrue(self.select(self.save())['backend'][0])
        (self.root / 'backend/b.go').unlink()
        self.assertTrue(self.select(self.save())['backend'][0])
        self.write('frontend/pnpm-lock.yaml', 'new dependency')
        self.assertTrue(self.select(self.save())['frontend'][0])

    def test_trigger_edits_skip_but_test_command_edits_run(self):
        p = self.root / '.github/workflows/main.yml'
        p.write_text(p.read_text() + 'on:\n  pull_request:\n')
        self.assertFalse(any(v[0] for v in self.select(self.save()).values()))
        p.write_text(p.read_text().replace('go test ./...', 'go test -race ./...'))
        result = self.select(self.save())
        self.assertTrue(result['backend'][0])
        self.assertFalse(result['frontend'][0])

    def test_successful_identical_inputs_reused_but_failed_jobs_not_reused(self):
        self.write('backend/a.go', 'package updated\n')
        previous = self.save()
        self.write('docs/notes.md', 'unrelated followup')
        head = self.save()
        class Evidence:
            def candidates(self, *args):
                return [{'id': 42, 'head_sha': previous}]
            def successful_jobs(self, run_id):
                return {'backend'}
        self.assertFalse(self.select(head, Evidence())['backend'][0])
        with patch.object(Evidence, 'successful_jobs', return_value=set()):
            self.assertTrue(self.select(head, Evidence())['backend'][0])
        self.write('backend/a.go', 'package newer\n')
        self.assertTrue(self.select(self.save(), Evidence())['backend'][0])

    def test_matrix_reuse_requires_every_shard_and_identical_matrix(self):
        p = self.root / '.github/workflows/main.yml'
        workflow = yaml.safe_load(p.read_text())
        workflow['jobs']['backend']['strategy'] = {'fail-fast': False, 'matrix': {'shard': [0, 1, 2, 3]}}
        p.write_text(yaml.safe_dump(workflow))
        self.write('backend/a.go', 'package updated\n')
        previous = self.save()
        self.write('docs/notes.md', 'unrelated')
        head = self.save()
        class Evidence:
            def candidates(self, *args):
                return [{'id': 42, 'head_sha': previous}]
            def successful_jobs(self, run_id):
                return {f'backend ({i})' for i in range(4)}
        self.assertFalse(self.select(head, Evidence())['backend'][0])
        for missing in range(4):
            with patch.object(Evidence, 'successful_jobs', return_value={f'backend ({i})' for i in range(4) if i != missing}):
                self.assertTrue(self.select(head, Evidence())['backend'][0])
        workflow['jobs']['backend']['strategy']['matrix']['shard'].append(4)
        p.write_text(yaml.safe_dump(workflow))
        self.assertTrue(self.select(self.save(), Evidence())['backend'][0])

    def test_api_failure_runs_affected_checks(self):
        self.write('backend/a.go', 'package updated\n')
        class Broken:
            def candidates(self, *args):
                raise TimeoutError('fixture')
        self.assertTrue(self.select(self.save(), Broken())['backend'][0])

    def test_manual_schedule_and_tags_force_full_checks(self):
        for event_name in ['workflow_dispatch', 'schedule']:
            result = ci.select('main.yml', self.scopes, event_name, {}, self.base)
            self.assertTrue(all(v[0] for v in result.values()))
        with patch.dict('os.environ', {'GITHUB_REF': 'refs/tags/v1.0.0'}):
            result = ci.select('main.yml', self.scopes, 'push', {}, self.base)
            self.assertTrue(all(v[0] for v in result.values()))

    def test_production_merge_reuses_identical_successful_pr_inputs(self):
        self.write('backend/a.go', 'package updated\n')
        head = self.save()
        class Evidence:
            def candidates(self, *args):
                return [{'id': 11, 'head_sha': head}]
            def successful_jobs(self, run_id):
                return {'backend'}
        result = ci.select('main.yml', self.scopes, 'push', {'before': self.base}, head, Evidence())
        self.assertFalse(result['backend'][0])

    def test_default_merge_checkout_is_not_reusable(self):
        p = self.root / '.github/workflows/main.yml'
        p.write_text(p.read_text().replace('          ref: ${{ github.event.pull_request.head.sha || github.sha }}', '          fetch-depth: 0'))
        self.write('backend/a.go', 'package updated\n')
        previous = self.save()
        self.write('docs/notes.md', 'unchanged application')
        head = self.save()
        class Evidence:
            def candidates(self, *args):
                return [{'id': 42, 'head_sha': previous}]
            def successful_jobs(self, run_id):
                return {'backend'}
        self.assertTrue(self.select(head, Evidence())['backend'][0])

    def test_official_sdk_fixture_change_selects_backend_integration(self):
        actual = json.loads((REPO / '.github/ci/scopes.json').read_text())
        self.scopes['backend'] = actual['backend-ci.yml']['integration-test']
        for path in ('.github/scripts/anthropic_integrity_sdk.py',
                     '.github/scripts/anthropic_integrity_sdk_requirements.txt'):
            with self.subTest(path=path):
                self.write(path, 'changed SDK fixture\n')
                head = self.save()
                result = self.select(head)
                self.assertTrue(result['backend'][0])
                self.assertFalse(result['frontend'][0])

    def test_other_pr_and_other_repository_results_are_not_candidates(self):
        evidence = ci.Evidence('fixture/repo', 'unused')
        common = {'head_sha': 'a' * 40, 'head_repository': {'full_name': 'fixture/repo'}, 'pull_requests': [{'number': 7}]}
        runs = [dict(common, id=1), dict(common, id=2, pull_requests=[{'number': 8}]),
                dict(common, id=3, head_repository={'full_name': 'other/repo'})]
        with patch.object(evidence, 'get', return_value={'workflow_runs': runs}):
            self.assertEqual([r['id'] for r in evidence.candidates('main.yml', self.event(), 'pull_request', self.base)], [1])


class WiringTests(unittest.TestCase):
    def test_workflow_outputs_scopes_and_conditions_are_consistent(self):
        config = json.loads((REPO / '.github/ci/scopes.json').read_text())
        for filename, scopes in config.items():
            workflow = yaml.safe_load((REPO / '.github/workflows' / filename).read_text())
            self.assertEqual(set(workflow['jobs']['changes']['outputs']), set(scopes), filename)
            for job in scopes:
                self.assertEqual(workflow['jobs'][job]['needs'], 'changes')
                self.assertEqual(workflow['jobs'][job]['if'], f"needs.changes.outputs['{job}'] == 'true'")
        for p in (REPO / '.github/workflows').glob('*.yml'):
            self.assertTrue(yaml.safe_load(p.read_text()).get('jobs'), p.name)

    def test_feature_workflow_jobs_unchanged_by_trigger_cleanup(self):
        # Broad inputs must not sneak back through shared UI metadata.
        for p in (REPO / '.github/workflows').glob('*.yml'):
            workflow = yaml.safe_load(p.read_text())
            events = workflow.get('on', workflow.get(True, {}))
            paths = (events.get('pull_request') or {}).get('paths', [])
            self.assertNotIn('frontend/src/constants/newFeatureNotices.ts', paths, p.name)
            self.assertNotIn('frontend/src/**', paths, p.name)


if __name__ == '__main__':
    unittest.main()
