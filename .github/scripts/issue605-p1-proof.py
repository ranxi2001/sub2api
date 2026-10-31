"""P1 proof accepts behavioral assertion failures only, never compile/fixture RED."""
import collections
import json
import pathlib
import re
import sys

mode, root = sys.argv[1], pathlib.Path(sys.argv[2])
summary = {}
problems = []
with (root / 'whitelist.jsonl').open('w') as out:
    for phase in ('service', 'integration'):
        counts = collections.Counter()
        actions, markers = {}, collections.defaultdict(set)
        path = root / (phase + '.jsonl')
        for line in path.read_text().splitlines() if path.exists() else []:
            try:
                item = json.loads(line)
            except ValueError:
                item = {'Action': 'output', 'Output': line}
            action, test = item.get('Action'), item.get('Test')
            safe = None
            if action in ('pass', 'fail', 'skip'):
                safe = {k: item[k] for k in ('Action', 'Package', 'Test', 'Elapsed') if k in item}
                counts[action] += 1
                if test:
                    actions[test] = action
            elif action in ('output', 'build-output'):
                text = item.get('Output', '')[:4096]
                found = re.findall(r'\bP1_[A-Z_]{1,80}\b', text)
                if found and test:
                    markers[test].update(found)
                safe = {'Test': test}
                if found:
                    safe['markers'] = found
                location = re.search(r'(?<![A-Za-z0-9_])([A-Za-z0-9_]{1,128}\.go):(\d{1,8})(?::(\d{1,8}))?', text)
                if location:
                    safe['location'] = ':'.join(v for v in location.groups() if v)
                compiler = re.search(r'(?:^|/)([A-Za-z0-9_.-]{1,128}\.go):\d{1,8}:\d{1,8}: (.{1,1000})', text)
                if compiler:
                    safe['compiler'] = re.sub(r'"[^"\n]*"|https?://\S+|sk-[\w-]+', '<literal>', compiler.group(2))
                causes = [name for text_part, name in [('WARNING: DATA RACE', 'race'), ('panic:', 'panic'), ('no tests to run', 'no_tests'), ('context deadline exceeded', 'deadline'), ('Received unexpected error:', 'unexpected_error')] if text_part in text]
                if causes:
                    safe['causes'] = causes
                if 'no_tests' in causes or 'race' in causes or 'panic' in causes:
                    problems.append(phase + ': invalid runtime result ' + ','.join(causes))
                if len(safe) == 1:
                    safe = None
            if safe:
                safe['phase'] = phase
                encoded = json.dumps(safe)
                out.write(encoded + '\n')
                print(encoded)
        prefix = 'TestSmartProtocolPassthroughPartialUsage' if phase == 'service' else 'TestSmartProtocolPassthroughPartialBilling'
        eof = prefix + ('/p1_eof/prior_false' if phase == 'service' else '/eof/prior_false')
        marker = 'P1_CURRENT_TURN_USAGE_LOST' if phase == 'service' else 'P1_AUTHORITATIVE_USAGE_NOT_BILLED'
        endings = ('p1_eof', 'p1_cancel', 'p1_drain', 'p1_control') if phase == 'service' else ('eof', 'client_cancel', 'drain_timeout', 'control_cancel')
        cases = [prefix + '/' + end + '/prior_' + prior for end in endings for prior in ('false', 'true')]
        if mode == 'red':
            for case in cases:
                if actions.get(case) != 'fail' or marker not in markers[case]:
                    problems.append(phase + ': intended observed-usage assertion RED missing: ' + case)
        else:
            if actions.get(prefix) != 'pass' or counts['fail'] or not counts['pass']:
                problems.append(phase + ': GREEN missing or failing')
            if phase == 'integration':
                cases += [prefix + '/' + name + '/prior_false' for name in ('missing_usage', 'normal_terminal', 'terminal_error')]
            for case in cases:
                if actions.get(case) != 'pass':
                    problems.append(phase + ': GREEN case missing: ' + case)
        if counts['skip']:
            problems.append(phase + ': skipped tests')
        summary[phase] = dict(counts=counts, actions=actions, assertion_markers={k: sorted(v) for k, v in markers.items()})
(root / 'summary.json').write_text(json.dumps(dict(mode=mode, phases=summary, problems=problems), indent=2) + '\n')
if problems:
    raise SystemExit('; '.join(problems))
