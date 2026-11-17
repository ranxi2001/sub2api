#!/usr/bin/env python3
"""Run the actual workflow gate commands, including statements after each gate."""
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
WORKFLOWS = ['oauth-degradation-actions.yml', 'oauth-degradation-native-controls.yml']
CONTAMINANTS = {
    'panic': 'panic: synthetic contaminant',
    'race': 'WARNING: DATA RACE',
    'race-test': 'race detected during execution of test',
    'fatal': 'fatal error: unexpected runtime failure',
    'signal': 'signal: killed',
    'segv': 'SIGSEGV: segmentation violation',
    'abort': 'SIGABRT: abort',
    'crash': 'Segmentation fault (core dumped)',
    'timeout': 'panic: test timed out after 1m',
    'skip': '--- SKIP: TestBusiness (0.00s)',
    'no-tests': 'testing: warning: no tests to run',
    'no-files': '? example/package [no test files]',
    'compile': 'FAIL example/package [build failed]',
    'setup': 'FAIL example/package [setup failed]',
    'undefined': 'fixture.go:1: undefined: unavailableSymbol',
    'syntax': 'fixture.go:1: syntax error: unexpected token',
    'ui-resolve': 'Failed to resolve import synthetic',
    'ui-transform': 'Transform failed with 1 error',
    'ui-no-tests': 'No test files found, exiting with code 1',
}
VALID = {
    'green': '=== RUN   TestBusiness\n--- PASS: TestBusiness (0.01s)\nPASS\nok example/package 0.01s\n',
    'red': '=== RUN   TestBusiness\n    business assertion: expected local day boundary\n--- FAIL: TestBusiness (0.01s)\nFAIL\nFAIL example/package 0.01s\n',
    'race-red': '=== RUN   TestBusiness\nWARNING: DATA RACE\n    race detected during execution of test\n--- FAIL: TestBusiness (0.01s)\nFAIL\n',
}


class WorkflowLogGateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.workspace = Path(self.temp.name)
        (self.workspace / 'evidence').mkdir()
        (self.workspace / '.github').symlink_to(ROOT / '.github', target_is_directory=True)
        (self.workspace / 'candidate').symlink_to(ROOT, target_is_directory=True)

    def commands(self):
        commands = []
        for workflow in WORKFLOWS:
            source = (ROOT / '.github/workflows' / workflow).read_text()
            self.assertNotRegex(source, r'(?m)^\s*!\s*grep\b')
            for line in source.splitlines():
                if 'bash ' in line and 'scripts/assert-test-log-clean.sh ' in line:
                    commands.append((workflow, line.strip()))
        # Guard against accidentally dropping any of the original critical gates.
        critical = {'retention-red.log', 'timezone-red.log', 'timezone-green.log',
                    'merge-scheduler-green.log', 'postgres-red.log', 'postgres.log',
                    'chain-red.log', 'reviewed-integration-race.log',
                    'timezone-integration-race.log', 'integration-race.log',
                    'service-unit.log', 'repository-unit.log', 'websocket-first-message.log',
                    'red.log', 'green.log', 'ui-red.log'}
        found = {Path(arg).name for _, command in commands for arg in shlex.split(command)[3:]}
        self.assertFalse(critical - found, critical - found)
        self.assertEqual(16, len(commands))
        return commands

    def run_gate(self, command, env=None):
        return subprocess.run(['bash', '-c', 'set -euo pipefail\n' + command + '\nprintf "ACCEPTED\\n"'],
                              cwd=self.workspace, env=env, text=True, capture_output=True)

    def seed(self, command):
        args = shlex.split(command)
        mode, logs = args[2], args[3:]
        for log in logs:
            (self.workspace / log).write_text(VALID[mode])
        return mode, logs

    def test_valid_logs_reach_next_statement_for_every_workflow_gate(self):
        for workflow, command in self.commands():
            with self.subTest(workflow=workflow, command=command):
                self.seed(command)
                result = self.run_gate(command)
                self.assertEqual(0, result.returncode, result.stderr)
                self.assertIn('ACCEPTED', result.stdout)

    def test_contamination_stops_before_next_statement_for_every_gate_and_log(self):
        count = 0
        for workflow, command in self.commands():
            mode, logs = self.seed(command)
            for log in logs:
                for name, contaminant in CONTAMINANTS.items():
                    if mode == 'race-red' and name in ('race', 'race-test'):
                        continue  # The original time.Local race is this RED's oracle.
                    with self.subTest(workflow=workflow, command=command, log=log, mutation=name):
                        self.seed(command)
                        with (self.workspace / log).open('a') as stream:
                            stream.write(contaminant + '\n')
                        result = self.run_gate(command)
                        self.assertNotEqual(0, result.returncode)
                        self.assertNotIn('ACCEPTED', result.stdout)
                        count += 1
        print(f'Executed {count} contaminant mutations against actual workflow gate commands')

    def test_missing_and_empty_logs_reject_instead_of_accepting_grep_errors(self):
        for _, command in self.commands():
            _, logs = self.seed(command)
            for log in logs:
                for missing in (True, False):
                    with self.subTest(command=command, log=log, missing=missing):
                        self.seed(command)
                        target = self.workspace / log
                        target.unlink() if missing else target.write_text('')
                        result = self.run_gate(command)
                        self.assertNotEqual(0, result.returncode)
                        self.assertNotIn('ACCEPTED', result.stdout)

    def test_inspection_failure_rejects(self):
        command = 'bash .github/scripts/assert-test-log-clean.sh green evidence/valid.log'
        self.seed(command)
        binary_dir = self.workspace / 'bin'
        binary_dir.mkdir()
        fake_grep = binary_dir / 'grep'
        fake_grep.write_text('#!/bin/sh\nexit 2\n')
        fake_grep.chmod(0o755)
        env = dict(os.environ, PATH=str(binary_dir) + os.pathsep + os.environ['PATH'])
        result = self.run_gate(command, env)
        self.assertEqual(2, result.returncode)
        self.assertNotIn('ACCEPTED', result.stdout)

    def test_failed_assertion_is_only_accepted_as_red(self):
        for mode in ('green', 'red'):
            command = f'bash .github/scripts/assert-test-log-clean.sh {mode} evidence/assertion.log'
            (self.workspace / 'evidence/assertion.log').write_text(VALID['red'])
            result = self.run_gate(command)
            self.assertEqual(mode == 'red', result.returncode == 0)


if __name__ == '__main__':
    unittest.main()
