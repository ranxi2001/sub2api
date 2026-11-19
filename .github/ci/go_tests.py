"""Run complete Go suites with deterministic package shards and timing artifacts."""
import argparse
from collections import Counter
import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path(__file__).resolve().parents[2]
BACKEND = ROOT / 'backend'


def partition(packages, total, weights):
    """Largest observed packages first; new packages get a nonzero default weight."""
    if total < 1 or len(packages) != len(set(packages)):
        raise ValueError('invalid shard count or duplicate packages')
    shards = [[] for _ in range(total)]
    loads = [0.0] * total
    for package in sorted(packages, key=lambda p: (-weights.get(p, 1.0), p)):
        index = min(range(total), key=lambda i: (loads[i], i))
        shards[index].append(package)
        loads[index] += weights.get(package, 1.0)
    return [sorted(shard) for shard in shards]


def collect(stream, events, log):
    """Stream output without retaining test logs in memory; preserve raw JSON."""
    terminals = []
    for line in stream:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            print(line, end='', flush=True)
            log.write(line)
            continue
        events.write(line)
        output = event.get('Output', '')
        if output:
            print(output, end='', flush=True)
            log.write(output)
        if event.get('Action') in ('pass', 'fail', 'skip'):
            terminals.append({key: event[key] for key in
                              ('Package', 'Test', 'Action', 'Elapsed') if key in event})
    return terminals


def run_command(command, output, name):
    started = time.monotonic()
    print('+ ' + ' '.join(command), flush=True)
    with (output / f'{name}.jsonl').open('w') as events, (output / f'{name}.log').open('w') as log:
        # Keep compiler stderr in the Actions log; stdout is the Go JSON stream.
        with subprocess.Popen(command, cwd=BACKEND, stdout=subprocess.PIPE,
                              text=True, encoding='utf-8', errors='replace') as process:
            terminals = collect(process.stdout, events, log)
            result = process.wait()
    return {'command': command, 'exit_code': result,
            'wall_seconds': round(time.monotonic() - started, 3), 'events': terminals}


def markdown(report):
    rows = [f"### Go tests: {report['suite']} shard {report['shard'] + 1}/{report['total']}", '',
            '| Command | Wall seconds | Exit code |', '| --- | ---: | ---: |']
    for result in report['runs']:
        rows.append(f"| `{result['command'][0]} {result['command'][-1]}` | {result['wall_seconds']} | {result['exit_code']} |")
    events = [e for r in report['runs'] for e in r['events']]
    rows += ['', f"Test outcomes (includes subtests): {dict(Counter(e['Action'] for e in events if 'Test' in e))}", '',
             'Go-reported elapsed time excludes compilation/setup; cached results may replay old timings.',
             'Package/test times can overlap and must not be added to obtain wall time.']
    for title, test in [('Slowest packages', False), ('Slowest tests', True)]:
        rows += ['', f'#### {title}', '', '| Package / test | Seconds | Result |', '| --- | ---: | --- |']
        selected = sorted((e for e in events if ('Test' in e) == test and 'Elapsed' in e),
                          key=lambda e: -e['Elapsed'])[:30]
        for e in selected:
            label = (e.get('Package', '') + (' / ' + e['Test'] if test else '')).replace('|', '&#124;')
            rows.append(f"| {label} | {e['Elapsed']} | {e['Action']} |")
    return '\n'.join(rows) + '\n'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('suite', choices=['unit', 'integration'])
    parser.add_argument('--shard', type=int, default=0)
    parser.add_argument('--total', type=int, default=1)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if not 0 <= args.shard < args.total or (args.suite == 'integration' and args.total != 1):
        parser.error('invalid shard; integration runs as one complete suite')
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    report = {'suite': args.suite, 'shard': args.shard, 'total': args.total, 'runs': []}
    status = 0
    try:
        if args.suite == 'unit':
            # Do not use process substitution: a failed go list must fail the job.
            packages = subprocess.check_output(['go', 'list', '-tags=unit', './...'], cwd=BACKEND, text=True).splitlines()
            if not packages:
                raise RuntimeError('go list returned no packages')
            weights = json.loads(Path(__file__).with_name('go-test-weights.json').read_text())['unit_seconds']
            shards = partition(packages, args.total, weights)
            (output / 'shards.json').write_text(json.dumps(shards, indent=2) + '\n')
            # test-unit normally includes this nested module prerequisite. Run once.
            if args.shard == 0:
                report['runs'].append(run_command(['make', 'test-collection', 'TEST_FLAGS=-json'], output, 'collection'))
            if shards[args.shard]:
                report['runs'].append(run_command(['make', 'test-unit-packages', 'TEST_FLAGS=-json -timeout=20m',
                                                  'TEST_PACKAGES=' + ' '.join(shards[args.shard])], output, 'unit'))
        else:
            report['runs'].append(run_command(['make', 'test-integration', 'TEST_FLAGS=-json -count=1'], output, 'integration'))
        status = int(any(r['exit_code'] != 0 for r in report['runs']))
    finally:
        (output / 'timings.json').write_text(json.dumps(report, indent=2) + '\n')
        summary = markdown(report)
        (output / 'summary.md').write_text(summary)
        if os.environ.get('GITHUB_STEP_SUMMARY'):
            with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as target:
                target.write(summary)
    return status


if __name__ == '__main__':
    raise SystemExit(main())
