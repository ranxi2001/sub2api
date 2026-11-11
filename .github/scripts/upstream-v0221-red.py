#!/usr/bin/env python3
"""Remote-only exact-head behavioral counterexamples for the release integration."""
import json
import os
import pathlib
import re
import subprocess
import sys

BASE = '6fa7fb86eae7c732794450646dc13831dce87ff1'
CASES = {
    'glm': ('internal/service', '^Test(MessagesGLMMappedEffortPolicyHTTP|OpenAIMessagesGLMReasoningPricingUsesForwardedEffort)$', 'backend/internal/service/openai_reasoning_effort_policy.go', 'applyOpenAIReasoningEffortPolicyForGLMFromContext'),
    'terra': ('internal/service', '^TestMessagesEffortPolicyHTTP$', 'backend/internal/service/openai_gateway_request_body.go', 'validateGPT61RawEffort'),
    'auth': ('internal/service', '^TestAuthServiceRefreshTokenPairConsumesTokenOnce$', 'backend/internal/service/auth_service.go', None),
    'slot': ('internal/service', '^TestGatewayServiceNewSelectionResultReleasesSlotOnHydrationError$', 'backend/internal/service/gateway_scheduling.go', None),
    'metadata': ('internal/service', '^TestRelayStallsMetadataBeforeCapacityFailure$', 'backend/internal/service/openai_gateway_passthrough.go', 'openAIStreamEventIsMetadata'),
    'session': ('internal/service', '^TestDeriveOpenAIContentSessionSeed_ResponsesLeadingPrefix$', 'backend/internal/service/openai_content_session_seed.go', None),
    'ttl': ('internal/service', '^TestOpenAICacheWriteTTLRecordUsage$', 'backend/internal/service/openai_gateway_response_handling.go', 'ttl-counterfactual'),
    'memory': ('internal/config', '^TestAutoRequestBodyMemoryBudget$', 'backend/internal/config/config.go', 'memory-counterfactual'),
    'sealed': ('internal/service', '^TestOpenAIResponsesSealedFunctionCallRoundTrip$', 'backend/internal/pkg/apicompat/responses_to_chatcompletions.go', None),
    'refund': ('internal/payment/provider', '^TestEasyPayRefundSuccessCode$', 'backend/internal/payment/provider/easypay.go', None),
}

def git(*args):
    return subprocess.check_output(['git', *args], text=True).strip()


def method(text, name):
    match = re.search(r'^func (?:\([^\n]+\) )?' + re.escape(name) + r'\(', text, re.M)
    if not match:
        raise RuntimeError('function not found: ' + name)
    end = re.search(r'^}', text[match.start():], re.M)
    if not end:
        raise RuntimeError('function end not found: ' + name)
    return match.start(), match.start() + end.end()


def run_case(package, pattern, directory, mode):
    result = subprocess.run(['go', 'test', '-json', '-tags=unit', './' + package, '-run', pattern,
                             '-count=1', '-timeout=5m'], cwd='backend', capture_output=True, text=True)
    output = result.stdout + result.stderr
    (directory / (mode + '.jsonl')).write_text(output)
    (directory / (mode + '-exit.txt')).write_text(str(result.returncode) + '\n')
    print(output, flush=True)
    events = []
    for line in result.stdout.splitlines():
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    tests = [e for e in events if e.get('Action') == 'run' and e.get('Test')]
    failed = [e for e in events if e.get('Action') == 'fail' and e.get('Test')]
    forbidden = r'\[build failed\]|\[setup failed\]|undefined:|syntax error:|no tests to run|panic:'
    if not tests or re.search(forbidden, output):
        raise RuntimeError(mode + ' is not behavioral test evidence')
    if any(e.get('Action') == 'skip' for e in events):
        raise RuntimeError(mode + ' unexpectedly skipped coverage')
    if mode == 'green' and (result.returncode != 0 or failed):
        raise RuntimeError('candidate GREEN failed')
    if mode == 'red' and (result.returncode == 0 or not failed):
        raise RuntimeError('old behavior did not fail regression')
    return {'exit': result.returncode, 'run_entries': len(tests), 'failed_entries': len(failed)}


def main():
    case = sys.argv[1]
    package, pattern, target, function = CASES[case]
    expected = os.environ['EXPECTED_HEAD_SHA']
    if git('rev-parse', 'HEAD') != expected or git('status', '--porcelain'):
        raise RuntimeError('must start at the clean exact candidate head')
    directory = pathlib.Path(os.environ['RUNNER_TEMP']) / ('v0221-proof-' + case)
    directory.mkdir(parents=True, exist_ok=True)
    source = pathlib.Path(target)
    candidate = source.read_text()
    baseline_ref = '811346e0256a75a978951beff0657a6a3bcf4a60' if case == 'glm' else BASE
    if case == 'terra':
        baseline_ref = 'e39708cc31091a8540c5b895c13add0842d4afc5'
    baseline = subprocess.check_output(['git', 'show', baseline_ref + ':' + target], text=True)
    if case == 'terra':
        # Restore the exact Sol-only raw validator; rename its declaration to
        # the candidate API so missing symbols cannot masquerade as RED.
        baseline = baseline.replace('func validateGPT61SolRawEffort(', 'func validateGPT61RawEffort(')
    evidence = {'case': case, 'candidate': expected, 'baseline': baseline_ref, 'target': target,
                'candidate_blob': git('hash-object', target), 'baseline_blob': git('rev-parse', baseline_ref + ':' + target),
                'command': 'go test -json -tags=unit ./' + package + ' -run ' + pattern + ' -count=1 -timeout=5m'}
    evidence['green'] = run_case(package, pattern, directory, 'green')
    if function in ('ttl-counterfactual', 'memory-counterfactual'):
        name = 'openAICacheCreationTTLTokensFromUsage' if case == 'ttl' else 'autoRequestBodyMemoryBudget'
        start, end = method(candidate, name)
        signature = candidate[start:candidate.index('{', start) + 1]
        # Baseline has no TTL fields/helper; reproduce its aggregate-only behavior
        # within the candidate API so a compiler failure cannot masquerade as RED.
        body = '\n\treturn 0, 0\n}' if case == 'ttl' else '\n\treturn int64(1 << 30)\n}'
        overlay = candidate[:start] + signature + body + candidate[end:]
        evidence['counterfactual'] = 'baseline aggregate-only TTL parsing' if case == 'ttl' else 'baseline fixed 1GiB default'
    elif function:
        a, b = method(candidate, function)
        x, y = method(baseline, function)
        overlay = candidate[:a] + baseline[x:y] + candidate[b:]
        evidence['restored_method'] = function
        if case == 'terra':
            evidence['baseline_method'] = 'validateGPT61SolRawEffort (declaration renamed only)'
    else:
        overlay = baseline
        evidence['restored_whole_file'] = True
    try:
        source.write_text(overlay)
        evidence['overlay_blob'] = git('hash-object', target)
        (directory / 'overlay.diff').write_text(subprocess.check_output(['git', 'diff', '--', target], text=True))
        evidence['red'] = run_case(package, pattern, directory, 'red')
    finally:
        source.write_text(candidate)
    if git('hash-object', target) != evidence['candidate_blob']:
        raise RuntimeError('candidate restoration failed')
    (directory / 'proof.json').write_text(json.dumps(evidence, indent=2) + '\n')


if __name__ == '__main__':
    main()
