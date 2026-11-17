import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import yaml

SPEC = importlib.util.spec_from_file_location('go_tests', Path(__file__).with_name('go_tests.py'))
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)


class GoTestRunnerTests(unittest.TestCase):
    def test_partition_is_complete_disjoint_and_separates_heavy_packages(self):
        packages = ['handler', 'service', 'repository', 'new-package', 'other']
        weights = {'handler': 580, 'service': 360, 'repository': 34}
        shards = runner.partition(packages, 4, weights)
        self.assertEqual(sorted(p for shard in shards for p in shard), sorted(packages))
        self.assertNotEqual(next(i for i, s in enumerate(shards) if 'handler' in s),
                            next(i for i, s in enumerate(shards) if 'service' in s))
        self.assertEqual(shards, runner.partition(list(reversed(packages)), 4, weights))
        self.assertEqual(runner.partition(['one'], 4, {}), [['one'], [], [], []])
        with self.assertRaises(ValueError):
            runner.partition(['duplicate', 'duplicate'], 4, {})

    def test_stream_keeps_raw_events_and_failed_skipped_and_nested_results(self):
        events = [
            {'Action': 'output', 'Package': 'p', 'Output': 'fixture output\n'},
            {'Action': 'pass', 'Package': 'p', 'Test': 'TestOne/child', 'Elapsed': 4},
            {'Action': 'skip', 'Package': 'p', 'Test': 'TestSkipped'},
            {'Action': 'fail', 'Package': 'p', 'Elapsed': 5},
        ]
        raw = 'make command\n' + ''.join(json.dumps(e) + '\n' for e in events)
        captured, log = io.StringIO(), io.StringIO()
        with patch('sys.stdout', io.StringIO()):
            terminals = runner.collect(io.StringIO(raw), captured, log)
        self.assertEqual([json.loads(line) for line in captured.getvalue().splitlines()], events)
        self.assertEqual(log.getvalue(), 'make command\nfixture output\n')
        self.assertEqual([e['Action'] for e in terminals], ['pass', 'skip', 'fail'])
        report = {'suite': 'unit', 'shard': 0, 'total': 4, 'runs': [
            {'command': ['fixture'], 'wall_seconds': 6, 'exit_code': 1, 'events': terminals}]}
        self.assertIn('TestOne/child', runner.markdown(report))
        self.assertIn('cached results', runner.markdown(report))

    def test_child_failure_survives_output_capture(self):
        with tempfile.TemporaryDirectory() as tmp, patch('sys.stdout', io.StringIO()):
            result = runner.run_command([sys.executable, '-c', 'print("compiler fixture"); raise SystemExit(7)'], Path(tmp), 'fail')
            self.assertEqual(result['exit_code'], 7)
            self.assertIn('compiler fixture', (Path(tmp) / 'fail.log').read_text())

    def test_nested_collection_runs_once_and_failure_cannot_be_masked(self):
        with tempfile.TemporaryDirectory() as tmp:
            def command(args, *unused):
                return {'command': args, 'exit_code': int('test-collection' in args), 'wall_seconds': 0, 'events': []}
            with patch.object(runner.subprocess, 'check_output', return_value='a\nb\nc\nd\n'), \
                    patch.object(runner, 'run_command', side_effect=command) as calls, \
                    patch.dict('os.environ', {'GITHUB_STEP_SUMMARY': ''}):
                codes = []
                for shard in range(4):
                    with patch.object(sys, 'argv', ['go_tests.py', 'unit', '--shard', str(shard), '--total', '4', '--output', tmp]):
                        codes.append(runner.main())
                self.assertEqual(codes, [1, 0, 0, 0])
                self.assertEqual(sum('test-collection' in c.args[0] for c in calls.call_args_list), 1)
                selected = [arg for c in calls.call_args_list for arg in c.args[0] if arg.startswith('TEST_PACKAGES=')]
                self.assertEqual(sorted(selected), ['TEST_PACKAGES=a', 'TEST_PACKAGES=b', 'TEST_PACKAGES=c', 'TEST_PACKAGES=d'])

    def test_discovery_failure_does_not_succeed_with_empty_suite(self):
        with tempfile.TemporaryDirectory() as tmp, \
                patch.object(runner.subprocess, 'check_output', side_effect=subprocess.CalledProcessError(1, 'go list')), \
                patch.object(sys, 'argv', ['go_tests.py', 'unit', '--output', tmp]), \
                patch.dict('os.environ', {'GITHUB_STEP_SUMMARY': ''}):
            with self.assertRaises(subprocess.CalledProcessError):
                runner.main()

    def test_both_suites_provision_native_redis_before_tests(self):
        # authcache integration tests spawn an isolated process; a Redis service
        # container alone cannot satisfy their exec.LookPath("redis-server").
        workflow = yaml.safe_load((runner.ROOT / '.github/workflows/backend-ci.yml').read_text())
        for job in ('unit-test', 'integration-test'):
            commands = [step.get('run', '') for step in workflow['jobs'][job]['steps']]
            install = next(i for i, command in enumerate(commands) if 'redis-server --version' in command)
            execute = next(i for i, command in enumerate(commands) if 'python3 .github/ci/go_tests.py' in command)
            self.assertLess(install, execute)
            self.assertIn('apt-get install -y --no-install-recommends redis-server', commands[install])

    def test_gate_rejects_failed_cancelled_or_missing_selected_suites(self):
        workflow = yaml.safe_load((runner.ROOT / '.github/workflows/backend-ci.yml').read_text())
        gate_job = workflow['jobs']['test']
        self.assertEqual(gate_job['if'], '${{ !cancelled() }}')
        gate = gate_job['steps'][0]['run']
        base = {'CHANGES_RESULT': 'success', 'UNIT_SELECTED': 'true', 'UNIT_RESULT': 'success',
                'INTEGRATION_SELECTED': 'true', 'INTEGRATION_RESULT': 'success'}
        def run(updates):
            return subprocess.run(['bash', '-e', '-c', gate], env=base | updates, capture_output=True).returncode
        self.assertEqual(run({}), 0)
        self.assertEqual(run({'UNIT_SELECTED': 'false', 'UNIT_RESULT': 'skipped'}), 0)
        for result in ['failure', 'cancelled', 'skipped', '']:
            self.assertNotEqual(run({'UNIT_RESULT': result}), 0)
            self.assertNotEqual(run({'INTEGRATION_RESULT': result}), 0)
        self.assertNotEqual(run({'CHANGES_RESULT': 'failure'}), 0)
        self.assertNotEqual(run({'UNIT_SELECTED': ''}), 0)


if __name__ == '__main__':
    unittest.main()
