#!/usr/bin/env python3
"""Remote-only RED/GREEN proof for the isolated escaped-content allocation guard."""
import difflib
import json
import os
import pathlib
import re
import subprocess
import time

TEST = 'TestOrphanCleanupFastRejectionLargeEscapedContent'
TARGET = 'backend/internal/service/openai_responses_input_compat.go'
WRAPPER = '''func openAIResponsesMayNeedOrphanCleanup(body []byte) bool {
\treturn openAIResponsesMayNeedOrphanCleanupWithTypes(body, codexToolCallOutputItemTypes[:])
}'''
COUNTERFACTUALS = {
    'predicate-true': '''func openAIResponsesMayNeedOrphanCleanup(body []byte) bool {
\treturn true
}''',
    'copy-decode-false': '''var orphanCleanupCounterfactualSink any

func openAIResponsesMayNeedOrphanCleanup(body []byte) bool {
\tcopied := bytes.Clone(body)
\tvar decoded any
\tif err := json.Unmarshal(copied, &decoded); err != nil {
\t\tpanic(err)
\t}
\torphanCleanupCounterfactualSink = decoded
\treturn false
}''',
}


def git(*args):
    return subprocess.check_output(['git', *args], text=True).strip()


def run_case(directory, mode, race, overlay=None):
    count = 3 if mode == 'green' else 1
    label = ('race-' if race else 'normal-') + mode
    command = ['go', 'test', '-json', '-tags=unit', './internal/service',
               '-run', '^' + TEST + '$', '-count=' + str(count), '-timeout=3m']
    if race:
        command.append('-race')
    if overlay:
        command.append('-overlay=' + str(overlay))
    started = time.monotonic()
    result = subprocess.run(command, cwd='backend', capture_output=True, text=True, timeout=900)
    elapsed = time.monotonic() - started
    output = result.stdout + result.stderr
    (directory / (label + '.jsonl')).write_text(output)
    (directory / (label + '-exit.txt')).write_text(str(result.returncode) + '\n')
    record = {'command': command, 'exit': result.returncode, 'elapsed_seconds': elapsed}
    (directory / (label + '-result.json')).write_text(json.dumps(record, indent=2) + '\n')
    print(label + ': ' + json.dumps(record), flush=True)
    print(output, flush=True)
    events = []
    for line in result.stdout.splitlines():
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    actions = [e.get('Action') for e in events if e.get('Test') == TEST]
    test_output = ''.join(e.get('Output', '') for e in events if e.get('Test') == TEST)
    measurements = re.findall(r'escaped allocation measurement N=(\d+) B/op=(-?\d+) elapsed=(\S+)', test_output)
    record['measurements'] = [{'N': int(n), 'B/op': int(b), 'elapsed': t} for n, b, t in measurements]
    forbidden = r'\[build failed\]|\[setup failed\]|no tests to run|panic:|fatal error:|test timed out'
    if actions.count('run') != count or re.search(forbidden, output) or any(e.get('Action') == 'skip' for e in events):
        raise RuntimeError(label + ': missing real test execution or non-assertion failure')
    if mode == 'green':
        if result.returncode != 0 or actions.count('pass') != count or 'fail' in actions:
            raise RuntimeError(label + ': candidate did not pass every repetition')
        if len(measurements) != count or any(int(n) <= 0 or not 0 <= int(b) <= 1024 for n, b, _ in measurements):
            raise RuntimeError(label + ': missing or invalid allocation measurements')
    else:
        if result.returncode == 0 or actions.count('fail') != 1:
            raise RuntimeError(label + ': counterfactual did not fail the outer test')
        if mode == 'predicate-true':
            reason = 'escaped message-only input unexpectedly requires orphan cleanup'
            if reason not in test_output:
                raise RuntimeError(label + ': missing predicate assertion failure')
        else:
            failure = re.search(r'escaped message-only scan allocated (\d+) bytes per call; maximum 1024', test_output)
            if not failure or int(failure[1]) <= 1024 or len(measurements) != 1 or int(measurements[0][0]) <= 0:
                raise RuntimeError(label + ': missing measured allocation assertion failure')
    return record


def main():
    if os.environ.get('GITHUB_ACTIONS') != 'true':
        raise RuntimeError('this proof may execute Go only in remote GitHub Actions')
    expected = os.environ['EXPECTED_HEAD_SHA']
    if git('rev-parse', 'HEAD') != expected or git('status', '--porcelain'):
        raise RuntimeError('must start at the clean exact candidate head')
    directory = pathlib.Path(os.environ['RUNNER_TEMP']) / 'v0221-orphan-alloc-proof'
    directory.mkdir(parents=True, exist_ok=True)
    source = pathlib.Path(TARGET).resolve()
    candidate = source.read_text()
    if candidate.count(WRAPPER) != 1:
        raise RuntimeError('counterfactual wrapper must match exactly once')
    proof = {'candidate': expected, 'source_blob': git('hash-object', TARGET),
             'go_version': subprocess.check_output(['go', 'version'], text=True).strip(), 'runs': {}}
    try:
        # These overlays compile the real predicate against the unchanged test
        # and fixture. Both must fail by assertion before accepting GREEN.
        for mode, replacement in COUNTERFACTUALS.items():
            mutant = candidate.replace(WRAPPER, replacement, 1)
            overlay_source = directory / (mode + '.go')
            overlay_source.write_text(mutant)
            overlay = directory / (mode + '-overlay.json')
            overlay.write_text(json.dumps({'Replace': {str(source): str(overlay_source)}}))
            (directory / (mode + '.diff')).write_text(''.join(difflib.unified_diff(
                candidate.splitlines(keepends=True), mutant.splitlines(keepends=True),
                fromfile=TARGET, tofile=mode)))
            for race in (False, True):
                label = ('race-' if race else 'normal-') + mode
                proof['runs'][label] = run_case(directory, mode, race, overlay)
        for race in (False, True):
            label = ('race-' if race else 'normal-') + 'green'
            proof['runs'][label] = run_case(directory, 'green', race)
        if git('rev-parse', 'HEAD') != expected or git('status', '--porcelain'):
            raise RuntimeError('candidate changed during proof')
    finally:
        (directory / 'proof.json').write_text(json.dumps(proof, indent=2) + '\n')


if __name__ == '__main__':
    main()
