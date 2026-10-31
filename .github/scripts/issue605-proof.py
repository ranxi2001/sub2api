"""Validate actual Go JSON test families; emit only bounded whitelist diagnostics."""
import collections
import json
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])
service = 'github.com/Wei-Shaw/sub2api/internal/service'
network = [
    'HTTPFirstSession', 'NetworkModes', 'ConcurrentSticky', 'NoReplayAndCleanup',
    'ForeignContinuation', 'TurnGuardBeforeSend', 'MissingUsageIsUnknown',
    'Legacy15MiB', 'StatelessRequestRetainsWS', 'PassthroughNetwork',
    'ToolContinuation', 'LegacyNoReplay', 'ActualContentNetwork', 'PassthroughNoReplay', 'PassthroughPartialUsage',
]
legacy = 'TestOpenAIWSHTTPBridgeAcceptsFirstFrameAboveLegacy16MiB'
required = {
    'packages': {(service, 'TestSmartProtocol' + n): 1 for n in network + [
        'Decision', 'History', 'AuthorizationMatrix', 'NativeCodexEndpointIsNotChainEvidence', 'PredictionSources']},
    'repeated': {(service, 'TestSmartProtocol' + n): 3 for n in network},
    'integration': {(service.replace('/service', '/repository'), n): 1 for n in [
        'TestSmartProtocolGatewayBillingAndQuotaIntegration', 'TestSmartProtocolTimingAndOwnershipReadback',
        'TestSmartProtocolPassthroughPartialBilling',
        'TestUsageBillingRepositoryApply_DeduplicatesBalanceBilling',
        'TestUsageBillingRepositoryApply_UpdatesAccountQuota',
        'TestSharedAccountAndUserConcurrencyAcrossIndependentClients',
        'TestIssue594ProductionGatewayConnectionRouteAndAccountSafety', 'TestGatewayCacheSuite']},
    'legacy-head': {(service, legacy): 1},
    'legacy-baseline': {(service, legacy): 1},
}
required['packages'].update({
    (service, 'TestAccountConnectionTimingLifecycle'): 1,
    ('github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming', 'TestAccountConnectionTimingClockAndPrivacy'): 1,
    ('github.com/Wei-Shaw/sub2api/internal/pkg/openai', 'TestSmartProtocolContentClassification'): 1,
    ('github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming', 'TestSmartProtocolTimingSnapshotCompatibility'): 1,
    ('github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming', 'TestSmartProtocolTurnTimingBoundAndOwnership'): 1,
})
causes = {
    'context deadline exceeded': 'context_deadline', 'connection refused': 'connection_refused',
    'i/o timeout': 'io_timeout', 'permission denied': 'permission_denied',
    'close of closed channel': 'closed_channel', 'invalid memory address': 'nil_pointer',
    'Expected value not to be nil': 'expected_non_nil', 'Not equal:': 'not_equal',
    'Received unexpected error:': 'unexpected_error', 'no tests to run': 'no_tests',
    'docker is not available': 'docker_unavailable', 'failed to apply db migrations': 'migration_failed',
}
summary, problems = {}, []
with (root / 'whitelist.jsonl').open('w') as out:
    def emit(item):
        out.write(json.dumps(item) + '\n')
        print(json.dumps(item))

    for phase, expected in required.items():
        counts = dict(passed=0, failed=0, skipped=0)
        passed = collections.Counter()
        stages = set()
        source = root / (phase + '.jsonl')
        for line in source.read_text().splitlines() if source.exists() else []:
            try:
                item = json.loads(line)
            except ValueError:
                item = {'Action': 'output', 'Output': line}
            action, test, package = item.get('Action'), item.get('Test'), item.get('Package')
            if action in ('output', 'build-output'):
                # Diagnostic matching must be linear/bounded even if an existing test
                # emits a multi-MiB synthetic frame. Never scan the full body.
                output = item.get('Output', '')[:4096]
                safe = {'phase': phase, 'Test': test}
                location = re.search(r'(?<![A-Za-z0-9_])([A-Za-z0-9_]{1,128}\.go):(\d{1,8})(?::(\d{1,8}))?', output)
                if location:
                    safe['location'] = ':'.join(v for v in location.groups() if v)
                compiler = re.search(r'(?:^|/)([A-Za-z0-9_.-]{1,128}\.go):\d{1,8}:\d{1,8}: (.{1,1000})', output)
                if compiler:
                    message = re.sub(r'https?://\S+|Bearer\s+\S+|sk-[\w-]+', '<redacted>', compiler.group(2))
                    safe['compiler'] = re.sub(r'"[^"\n]*"', '"<literal>"', message)[:1000]
                found = [value for text, value in causes.items() if text in output]
                if 'WARNING: DATA RACE' in output or 'race detected during execution' in output:
                    found.append('race_detected')
                if 'panic:' in output:
                    found.append('panic')
                if found:
                    safe['causes'] = found
                if 'no_tests' in found:
                    problems.append(phase + ': no tests to run')
                state = re.search(r'(expected|actual)\s*:\s*(true|false|nil|[0-9.]+|"(?:http|ws|auto|shadow|tool_input|visible_text|reasoning_text|empirical_p90|heuristic_cap|cold_start)")\s*$', output)
                if state:
                    safe[state.group(1)] = state.group(2)
                sqlstate = re.search(r'SQLSTATE ([0-9A-Z]{5})', output)
                if sqlstate:
                    safe['sqlstate'] = sqlstate.group(1)
                probe = re.search(r'ISSUE605_LEGACY_PROBE stage=(client_frame_written|ingress_frame_received|before_turn|http_send|http_received|after_turn|client_terminal) elapsed_ms=(\d+) calls=(\d+)', output)
                if probe:
                    safe['legacy_stage'], safe['elapsed_ms'], safe['calls'] = probe.group(1), int(probe.group(2)), int(probe.group(3))
                    stages.add(probe.group(1))
                if len(safe) > 2:
                    emit(safe)
            if action not in ('pass', 'fail', 'skip'):
                continue
            emit(dict(phase=phase, **{k: item[k] for k in ('Action', 'Package', 'Test', 'Elapsed') if k in item}))
            if test:
                counts[{'pass': 'passed', 'fail': 'failed', 'skip': 'skipped'}[action]] += 1
                if action == 'pass':
                    passed[(package, test)] += 1
            elif action == 'fail':
                counts['failed'] += 1
        missing = [f'{pkg}:{test} ({passed[(pkg, test)]}/{n})' for (pkg, test), n in expected.items() if passed[(pkg, test)] < n]
        if missing:
            problems.append(phase + ': missing required families: ' + ', '.join(missing))
        if phase == 'packages':
            packages = {p for p, _ in passed}
            for path in ('service', 'handler', 'repository', 'pkg/requesttiming', 'pkg/openai', 'service/openai_ws_v2'):
                if 'github.com/Wei-Shaw/sub2api/internal/' + path not in packages:
                    problems.append('missing actual package tests: ' + path)
        if phase.startswith('legacy-') and stages != {'client_frame_written', 'ingress_frame_received', 'before_turn', 'http_send', 'http_received', 'after_turn', 'client_terminal'}:
            problems.append(phase + ': incomplete real send lifecycle')
        if counts['failed'] or counts['skipped']:
            problems.append(phase + ': failures or skipped tests')
        summary[phase] = dict(**counts, required_families=len(expected), missing=missing)
(root / 'summary.json').write_text(json.dumps(dict(phases=summary, problems=problems), indent=2) + '\n')
if problems:
    for problem in problems:
        print(problem)
    raise SystemExit('acceptance incomplete')
