#!/usr/bin/env python3
"""Collect attributable remote execution, without equating tests to full acceptance."""
import collections
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

MANIFEST = Path(__file__).with_suffix('.json')


def main():
    mode = sys.argv[1]
    features = json.loads(MANIFEST.read_text())
    if mode == 'frontend-list':
        paths = {p for f in features for p in f['frontend']}
        paths.update(['src/components/common/__tests__/NewFeatureBadge.spec.ts',
                      'src/stores/__tests__/newFeatureNotices.spec.ts'])
        print('\n'.join(sorted(paths)))
        return
    if mode not in ('unit', 'integration', 'embed'):
        raise ValueError('unsupported mode')
    head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip()
    if head != os.environ['EXPECTED_HEAD_SHA']:
        raise RuntimeError('checkout is not the exact candidate head')
    destination = Path(os.environ['EVIDENCE_DIR'])
    destination.mkdir(parents=True, exist_ok=True)
    identity = {'head': head, 'mode': mode, 'run': os.environ['GITHUB_RUN_ID'],
                'attempt': os.environ['GITHUB_RUN_ATTEMPT'],
                'manifest_sha256': hashlib.sha256(MANIFEST.read_bytes()).hexdigest()}
    (destination / 'identity.json').write_text(json.dumps(identity, indent=2))
    packages = collections.defaultdict(set)
    for feature in features:
        for test in feature['tests']:
            if test['mode'] == mode:
                packages[test['package']].add(test['name'])
    observations = {}
    failures = []
    commands = []
    for package, roots in sorted(packages.items()):
        pattern = '^(' + '|'.join(re.escape(r) for r in sorted(roots)) + ')$'
        tags = 'unit,embed' if mode == 'embed' else mode
        command = ['go', 'test', '-race', '-json', '-tags=' + tags,
                   './' + package, '-run', pattern, '-count=1', '-timeout=15m']
        commands.append(command)
        raw = destination / (package.replace('/', '-') + '.jsonl')
        print('Running', mode, package, len(roots), 'expected roots', flush=True)
        seen = {}
        skipped = []
        with raw.open('w') as log:
            process = subprocess.Popen(command, cwd='backend', text=True,
                                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            for line in process.stdout:
                log.write(line)
                try:
                    event = json.loads(line)
                except json.JSONDecodeError:
                    print(line, end='', flush=True)
                    continue
                name, action = event.get('Test'), event.get('Action')
                if name and action in ('pass', 'fail', 'skip'):
                    if '/' not in name:
                        seen[name] = action
                    if action == 'skip':
                        skipped.append(name)
                    if action == 'fail':
                        print('FAIL', name, flush=True)
                if event.get('Output') and ('Error:' in event['Output'] or 'panic:' in event['Output']):
                    print(event['Output'], end='', flush=True)
            status = process.wait()
        for root in sorted(roots):
            observations[package + ':' + root] = seen.get(root, 'missing')
        bad = {root: seen.get(root, 'missing') for root in roots if seen.get(root) != 'pass'}
        if status or bad or skipped:
            failures.append({'package': package, 'exit': status, 'roots': bad, 'skipped': skipped})
        print(package, 'exit', status, 'outcomes', dict(collections.Counter(seen.values())), flush=True)
    summary = dict(identity, commands=commands, failures=failures, features=[])
    for feature in features:
        observed = {t['package'] + ':' + t['name']: observations.get(t['package'] + ':' + t['name'], 'missing')
                    for t in feature['tests'] if t['mode'] == mode}
        summary['features'].append({'id': feature['id'], 'observations': observed,
                                    'scope_gate': feature['scope_gate'],
                                    'full_acceptance': 'pending separate gate review'})
    (destination / 'results.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2))
    if failures:
        raise SystemExit(1)


if __name__ == '__main__':
    main()
