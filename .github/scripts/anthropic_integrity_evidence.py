"""Validate actual business failures/passes, never compilation or skipped tests."""
import json
import pathlib
import re
import sys


def required_tests(mode):
    tests = []
    for route in ('chat', 'responses'):
        tests += [f'TestIssue985Framing/{route}/{case}' for case in ('canonical', 'id', 'comment', 'multidata')]
        tests += [f'TestIssue985Failure/{route}/{case}' for case in ('empty', 'terminal_only', 'missing_usage', 'stop_reason_error')]
    if mode == 'red':
        return tests
    for route in ('chat', 'responses', 'native_chat', 'native_responses'):
        tests += [f'TestIssue985CanceledBeforeBufferedRead/{route}', f'TestIssue985BufferedFinalWriteFailure/{route}']
        for flow in ('stream', 'buffered'):
            tests += [f'TestIssue985AdapterBoundaries/{route}/{flow}/{case}' for case in ('crlf', 'cr', 'bom', 'data_only', 'field_order', 'event_without_data', 'compact', 'eof_frame', 'unknown', 'zero', 'missing', 'field_missing', 'stop_reason_only', 'terminal_error', 'malformed', 'mismatch')]
    for route in ('native', 'passthrough', 'normal'):
        tests += [f'TestIssue985RawPartialUsage/{route}']
        tests += [f'TestIssue985NativeRelays/{route}/{case}' for case in ('multidata', 'unknown_multidata', 'empty', 'missing', 'error_after_terminal', 'reasoning')]
    tests += ['TestIssue985EveryTransportSplit', 'TestIssue985FrameBoundAndFieldOrder']
    tests += [f'TestIssue985FinalWriteFailure/{route}' for route in ('chat_completions', 'responses')]
    for flow in ('true', 'false'):
        tests += [f'TestIssue985HandlerUsage/{case}/stream={flow}' for case in ('framed', 'missing', 'zero', 'partial_error', 'partial_protocol')]
        tests += [f'TestIssue985PostgresBilling/{case}/stream={flow}' for case in ('valid', 'missing', 'explicit_zero', 'partial_protocol', 'partial_error')]
        tests += [f'TestIssue985HandlerToolContinuation/stream={flow}']
        tests += [f'TestIssue985SDKAccumulator/{case}/stream={flow}' for case in ('text', 'tool', 'zero', 'missing', 'partial_error', 'partial_protocol')]
        tests += [f'TestIssue985CancellationResources/cancel={flow}']
    for route in ('chat', 'responses', 'native_chat', 'native_responses', 'native_messages', 'passthrough'):
        tests += [f'TestIssue985FlushFailure/{route}/{phase}' for phase in ('intermediate', 'final')]
    for suite in ('TestIssue985NativeHandlerUsage', 'TestIssue985NativePostgresBilling'):
        for route in ('chat', 'responses', 'messages'):
            for case in ('unknown', 'explicit_zero', 'input_only', 'output_only', 'cache_only', 'partial_full_error'):
                tests += [f'{suite}/{route}/{case}/stream={flow}' for flow in ('true', 'false')]
    return tests


def validate(mode, root):
    raw = (root / f'{mode}.jsonl').read_text()
    stderr = (root / f'{mode}.stderr').read_text()
    try:
        events = [json.loads(line) for line in raw.splitlines() if line.strip()]
    except ValueError as exc:
        raise AssertionError('non-JSON/corrupt Go test output') from exc
    output = ''.join(event.get('Output', '') for event in events) + stderr
    (root / f'{mode}.log').write_text(output)
    assert not re.search(r'WARNING: DATA RACE|build failed|setup failed|no tests to run|panic[: ]|fatal error:|runtime error:|SIGSEGV|test timed out', output), output[-8000:]
    assert not any(event.get('Action') == 'skip' for event in events), 'skip contamination'
    outcomes = {event['Test']: event['Action'] for event in events if event.get('Test') and event.get('Action') in ('pass', 'fail')}
    expected = {test: 'pass' for test in required_tests(mode)}
    if mode == 'red':
        expected = {test: ('pass' if test.endswith('/canonical') else 'fail') for test in expected}
    for test, outcome in expected.items():
        assert outcomes.get(test) == outcome, (test, outcome, outcomes.get(test))
    rc = int((root / f'{mode}.exitcode').read_text())
    assert rc == (1 if mode == 'red' else 0), rc
    if mode == 'red':
        assert output.count('legal SSE must preserve input 17') == 6, 'RED must fail at business 17/3 oracle'
        assert output.count('incomplete or errored stream must fail') == 8, 'RED must demonstrate false success'
    else:
        assert all(action == 'pass' for action in outcomes.values()), outcomes
    print(f'{mode}: validated {len(expected)} required business cases; exit={rc}')


if __name__ == '__main__':
    validate(sys.argv[1], pathlib.Path(sys.argv[2]))
