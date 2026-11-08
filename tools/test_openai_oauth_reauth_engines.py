import io
import json
import os
import sys
import tempfile
import time
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock, patch
from urllib.error import HTTPError

from tools import openai_oauth_reauth_worker as worker


class ChunkResponse:
    def __init__(self, chunks):
        self.chunks = iter(chunks)
        self.closed = False
    def read1(self, size):
        return next(self.chunks, b"")
    def __enter__(self):
        return self
    def __exit__(self, *_args):
        self.closed = True


def parallel_probe(config, stop=None):
    root = Path(config.base_url)
    (root / config.worker_id).touch()
    deadline = time.monotonic() + 3
    while len(list(root.glob('probe-slot-*'))) < 3 and time.monotonic() < deadline:
        time.sleep(.01)
    if len(list(root.glob('probe-slot-*'))) == 3:
        (root / ('done-' + config.worker_id)).touch()
    time.sleep(.4)


class ReauthEngineTests(unittest.TestCase):
    def test_operation_fixtures_pass_existing_worker_failure_boundary(self):
        cases = json.loads((Path(__file__).parent / 'fixtures' / 'credential-operation-errors.json').read_text())
        for case in cases:
            with self.subTest(name=case['name']):
                self.assertEqual(worker.credential_operation_reason(case['payload']), case['code'])
                api = self.api()
                api.claim = Mock(return_value=self.claim())
                api.fail = Mock()
                payload = {**case['payload'], 'status': 'failed'}
                raw = json.dumps({'type': 'result', 'payload': payload}).encode()
                with patch.object(worker.NO_REDIRECT_OPENER, 'open', return_value=ChunkResponse([raw])), self.assertLogs(worker.LOGGER, level='ERROR') as logs:
                    worker.run_once(api, None)
                expected = case['code'] if case['code'] != 'unknown' else 'provider_failed'
                self.assertIn('reason=' + expected + ' ', api.fail.call_args.args[1])
                self.assertNotIn('synthetic-private', str(logs.output) + api.fail.call_args.args[1])
                api.credentials.assert_not_called()

    def test_failure_boundary_drops_unlabelled_sensitive_data(self):
        for raw in ('password synthetic-private', 'token synthetic-private', 'totp synthetic-private',
                    'cookie synthetic-private', 'phone synthetic-private', 'https://example.test/?code=synthetic-private',
                    'Your account has not been deactivated.', 'User said: "account_deleted"', 'x' * 10000):
            self.assertEqual(worker.safe_worker_failure(worker.WorkerError(raw)), 'worker_failed')

    def test_real_api_failure_report_preserves_bounded_elapsed_and_drops_secrets(self):
        api = worker.WorkerAPI(SimpleNamespace(worker_id='fixture-worker'))
        api._post = Mock()
        for diagnostic in (
            'local_protocol_failed stage=web_login reason=phone_verification_required elapsed_ms=123456 exit=1',
            'session_studio_failed stage=terminal_result reason=account_retired elapsed_ms=123456 http_status=0',
        ):
            api.fail(7, diagnostic)
            self.assertEqual(api._post.call_args.args[1]['reason'], diagnostic)
        api.fail(7, 'token password cookie totp phone synthetic-private')
        self.assertEqual(api._post.call_args.args[1]['reason'], 'worker_failed')

    def test_email_adapter_ignores_external_guessed_codes_and_parses_actual_envelopes(self):
        cases = json.loads((Path(__file__).parent / 'fixtures' / 'credential-operation-errors.json').read_text())
        for case in cases:
            with self.subTest(name=case['name']):
                response = SimpleNamespace(text=json.dumps(case['payload']))
                self.assertEqual(worker.operation_response_reason(response), case['code'])
        for raw in ('{"error":{"code":"account_deleted","code":"account_retired"}}',
                    '{"error":{"co\\u0064e":"account_deleted","code":"account_retired"}}', 'x' * 32769):
            self.assertEqual(worker.operation_response_reason(SimpleNamespace(text=raw)), 'unknown')
        class ExternalGuessedError(Exception):
            error_code = 'account_deleted'
        self.assertEqual(worker.safe_worker_failure(ExternalGuessedError('User said: "account_deleted"')), 'worker_failed')
        self.assertEqual(worker.safe_worker_failure(worker.OperationWorkerError('account_retired')), 'account_retired')

    def test_email_transient_retry_budget_and_manual_verification_stop_are_preserved(self):
        for code, attempts in (('rate_limited', 3), ('transport_error', 3), ('phone_verification_required', 1),
                               ('security_check', 1), ('authorization_denied', 1), ('account_retired', 1)):
            with self.subTest(code=code):
                protocol = SimpleNamespace(ProtocolSession=Mock(), network_preflight=Mock(), bootstrap_authorize=Mock(),
                    submit_email=Mock(return_value={}), submit_email_otp=Mock(side_effect=worker.OperationWorkerError(code)),
                    resend_email_otp=Mock(), AccountUnusableError=type('ExternalAccountError', (Exception,), {}))
                api = SimpleNamespace(progress=Mock(), callback=Mock())
                claim = dict(task_id=7, account_id=42, credential_mode='email_otp_url', login_email='fixture',
                    otp_url='https://fixture.invalid/code', auth_url='https://auth.openai.com/oauth/authorize?state=synthetic')
                with patch.object(worker, 'OTPURLProvider'), patch.object(worker, '_wait_for_otp', return_value='000001'), self.assertRaises(worker.OperationWorkerError):
                    worker.process_claim(api, protocol, claim)
                self.assertEqual(protocol.submit_email_otp.call_count, attempts)
                self.assertEqual(protocol.resend_email_otp.call_count, attempts-1)
                api.callback.assert_not_called()

    def fixture(self, name):
        return (Path(__file__).parent / 'fixtures' / 'session-studio' / name).read_bytes()

    def test_duplicate_fields_cannot_overwrite_terminal_failure(self):
        for name in ('duplicate-error.ndjson', 'duplicate-status.ndjson',
                     'duplicate-payload.ndjson', 'escaped-duplicate-error.ndjson'):
            raw = self.fixture(name)
            # Every fixture would be accepted by the old last-key-wins decoder.
            legacy = json.loads(raw)['payload']
            self.assertFalse(legacy.get('error'))
            self.assertNotEqual(legacy.get('status'), 'failed')
            self.assertEqual(legacy['credential'], self.credentials())
            for chunks in ([raw], [bytes([c]) for c in raw]):
                with self.subTest(name=name, chunks=len(chunks)):
                    api = self.api()
                    response = ChunkResponse(chunks)
                    with patch.object(worker.NO_REDIRECT_OPENER, 'open', return_value=response), self.assertRaisesRegex(
                        worker.SessionStudioError, '^Session Studio returned ambiguous JSON$'
                    ) as failure:
                        worker.process_password_claim(api, self.claim())
                    self.assertEqual(failure.exception.reason, 'duplicate_field')
                    api.credentials.assert_not_called()
                    self.assertTrue(response.closed)

    def test_offline_fixture_result_and_failure_are_not_interchangeable(self):
        for name, success in (('progress-success.ndjson', True), ('provider-failed.ndjson', False)):
            with self.subTest(name=name):
                api = self.api()
                response = ChunkResponse([self.fixture(name)])
                with patch.object(worker.NO_REDIRECT_OPENER, 'open', return_value=response):
                    if success:
                        worker.process_password_claim(api, self.claim())
                        api.credentials.assert_called_once_with(7, self.credentials(), {})
                    else:
                        with self.assertRaises(worker.SessionStudioError) as failure:
                            worker.process_password_claim(api, self.claim())
                        self.assertEqual(failure.exception.stage, 'terminal_result')
                        self.assertEqual(failure.exception.reason, 'provider_failed')
                        api.credentials.assert_not_called()

    def test_run_once_reports_only_observed_allowlisted_phase(self):
        cases = (
            (self.fixture('provider-failed.ndjson'), 'terminal_result', 'provider_failed'),
            (self.fixture('duplicate-error.ndjson'), 'event_stream', 'duplicate_field'),
            (b'{"type":"heartbeat"}', 'event_stream', 'missing_result'),
            (b'{"type":"result","payload":null}', 'terminal_result', 'invalid_result'),
            (b'{"credential":{"access_token":"synthetic-private"}}', 'credentials_validation', 'credentials_incomplete'),
        )
        for raw, stage, reason in cases:
            with self.subTest(stage=stage, reason=reason):
                api = self.api()
                api.claim = Mock(return_value=self.claim())
                api.fail = Mock()
                with patch.object(worker.NO_REDIRECT_OPENER, 'open', return_value=ChunkResponse([raw])), patch.object(
                    worker.subprocess, 'run'
                ) as local, self.assertLogs(worker.LOGGER, level='ERROR') as logs:
                    self.assertTrue(worker.run_once(api, None))
                task_id, diagnostic = api.fail.call_args.args
                self.assertEqual(task_id, 7)
                self.assertRegex(diagnostic, rf'^session_studio_failed stage={stage} reason={reason} elapsed_ms=[0-9]+ http_status=0$')
                self.assertNotIn('synthetic', diagnostic + str(logs.output))
                api.credentials.assert_not_called()
                local.assert_not_called()

    def test_http_rejection_never_reads_body_or_retries(self):
        for code in (302, 401, 403, 429, 500):
            with self.subTest(code=code):
                api = self.api()
                api.claim = Mock(return_value=self.claim())
                api.fail = Mock()
                body = Mock()
                error = HTTPError('https://session.example/', code, 'synthetic-private', {}, body)
                with patch.object(worker.NO_REDIRECT_OPENER, 'open', side_effect=error) as opener, patch.object(
                    worker.subprocess, 'run'
                ) as local:
                    self.assertTrue(worker.run_once(api, None))
                self.assertRegex(api.fail.call_args.args[1], rf'^session_studio_failed stage=http_request reason=http_rejected elapsed_ms=[0-9]+ http_status={code}$')
                body.read.assert_not_called()
                opener.assert_called_once()
                local.assert_not_called()
                api.credentials.assert_not_called()

    def test_submission_failure_remains_failed_and_private(self):
        for result in ({'status': 'failed', 'error': 'synthetic-private'}, None, worker.WorkerError('synthetic-private')):
            with self.subTest(result=type(result).__name__):
                api = self.api()
                if isinstance(result, Exception):
                    api.credentials.side_effect = result
                else:
                    api.credentials.return_value = result
                api.claim = Mock(return_value=self.claim())
                api.fail = Mock()
                with patch.object(worker.NO_REDIRECT_OPENER, 'open', return_value=ChunkResponse([self.fixture('progress-success.ndjson')])):
                    self.assertTrue(worker.run_once(api, None))
                diagnostic = api.fail.call_args.args[1]
                self.assertIn('stage=credentials_submit', diagnostic)
                self.assertIn('reason=submission_failed' if isinstance(result, Exception) else 'reason=credentials_not_accepted', diagnostic)
                self.assertNotIn('synthetic', diagnostic)
                api.credentials.assert_called_once_with(7, self.credentials(), {})

    def test_non_json_constants_are_not_successful_credentials(self):
        for constant in (b'NaN', b'Infinity', b'-Infinity'):
            with self.subTest(constant=constant), self.assertRaises(worker.SessionStudioError):
                worker._session_studio_result(ChunkResponse([b'{"credential":{},"error":' + constant + b'}']))

    def test_diagnostic_survives_real_fail_serialization_without_unmasking_secrets(self):
        cfg = worker.WorkerConfig('http://127.0.0.1', 'x' * 32, 'synthetic-worker', None)
        api = worker.WorkerAPI(cfg)
        for elapsed in (0, 99999, 100000, 123456, 999999, 1000000, 3600000):
            diagnostic = f'session_studio_failed stage=terminal_result reason=provider_failed elapsed_ms={elapsed} http_status=0'
            with self.subTest(elapsed=elapsed), patch.object(api, '_post') as post:
                api.fail(7, diagnostic)
                self.assertEqual(post.call_args.args[1], {'worker_id': 'synthetic-worker', 'reason': diagnostic})
        for raw in (
            diagnostic + ' synthetic-private',
            diagnostic.replace('terminal_result', 'synthetic-private'),
            diagnostic.replace('provider_failed', 'token_exchange_failed'),
            diagnostic.replace('3600000', '3600001'),
            diagnostic.replace('http_status=0', 'http_status=403'),
        ):
            self.assertEqual(worker.sanitize_error(raw), 'Session Studio failed; invalid diagnostic omitted')

    def claim(self):
        return dict(task_id=7, account_id=42, credential_mode='password_totp',
                    engine='session_studio', login_email='demo@example.com',
                    password='synthetic-password', totp_secret='synthetic-totp',
                    relogin_endpoint='https://session.example/api/v1/relogin',
                    relogin_headers={'X-Session-Studio-Relogin':'1','X-Session-Studio-Client':'{{uuid}}'})
    def credentials(self):
        return dict(access_token='synthetic-access',refresh_token='synthetic-refresh',id_token='synthetic-id')
    def api(self):
        return SimpleNamespace(progress=Mock(), credentials=Mock(return_value={'status':'succeeded'}))
    def test_stream_stops_on_terminal_result_without_eof(self):
        # A socket would stay open for later heartbeats. Reading after this
        # terminal result is an error, rather than an artificial EOF.
        response = Mock()
        raw=json.dumps({'type':'result','payload':{'credential':self.credentials()}}).encode()
        response.read1.side_effect=[b'{"type":"heartbeat"}\n',raw[:20],raw[20:],AssertionError('waited for EOF')]
        self.assertEqual(worker._session_studio_result(response)['credential'],self.credentials())
        self.assertEqual(response.read1.call_count,3)
    def test_plain_json_and_utf8_split(self):
        raw=json.dumps({'credential':self.credentials(),'label':'中文'},ensure_ascii=False).encode()
        response=ChunkResponse([bytes([c]) for c in raw])
        self.assertEqual(worker._session_studio_result(response)['label'],'中文')
    def test_progress_and_null_errors_do_not_complete_login(self):
        notifications = (
            {'type': 'progress', 'status': 'active'},
            {'type': 'progress', 'error': None},
            {'type': 'progress', 'error': {'message': 'synthetic-diagnostic'}},
            {'type': 'progress', 'status': 'failed', 'error': 'synthetic-diagnostic'},
            {'type': 'heartbeat', 'status': 'active'},
            {'type': 'log', 'message': 'synthetic-notification'},
            {'status': 'active'},
            {'error': None},
            {'status': 'active', 'error': None},
        )
        terminal = {'type': 'result', 'payload': {'credential': self.credentials()}}
        for notification in notifications:
            with self.subTest(notification=notification):
                api = self.api()
                response = ChunkResponse([json.dumps(notification).encode() + b'\n',
                                          json.dumps(terminal).encode()])
                with patch.object(worker.NO_REDIRECT_OPENER, 'open', return_value=response), patch.object(
                    worker.subprocess, 'run'
                ) as local:
                    worker.process_password_claim(api, self.claim())
                api.credentials.assert_called_once_with(7, self.credentials(), {})
                local.assert_not_called()
                self.assertTrue(response.closed)
    def test_terminal_failure_cannot_be_replaced_by_later_success(self):
        failures = (
            {'type': 'result', 'payload': {'status': 'failed', 'error': 'synthetic-private'}},
            {'type': 'result', 'payload': {'error': {'message': 'synthetic-private'}}},
            {'type': 'result', 'payload': {'status': 'failed', 'credential': self.credentials()}},
            {'type': 'result', 'payload': {'credential': self.credentials(), 'error': 'synthetic-private'}},
            {'status': 'failed'},
            {'status': 'auth_failed', 'error': None},
            {'status': 'error'},
            {'error': {'message': 'synthetic-private'}},
        )
        success = {'type': 'result', 'payload': {'credential': self.credentials()}}
        for terminal in failures:
            for buffered in (False, True):
                with self.subTest(terminal=terminal, buffered=buffered):
                    api = self.api()
                    response = Mock()
                    response.__enter__ = Mock(return_value=response)
                    response.__exit__ = Mock(return_value=False)
                    raw = json.dumps(terminal).encode() + b'\n'
                    if buffered:
                        raw += json.dumps(success).encode()
                    response.read1.side_effect = [raw, AssertionError('read past terminal failure')]
                    with patch.object(worker.NO_REDIRECT_OPENER, 'open', return_value=response), patch.object(
                        worker.subprocess, 'run'
                    ) as local, self.assertRaisesRegex(
                        worker.WorkerError, '^Session Studio login failed$'
                    ):
                        worker.process_password_claim(api, self.claim())
                    api.credentials.assert_not_called()
                    local.assert_not_called()
                    self.assertEqual(response.read1.call_count, 1)
    def test_terminal_success_requires_complete_oauth_credentials(self):
        for missing in self.credentials():
            for value in (None, '', ' ', 123):
                with self.subTest(missing=missing, value=value):
                    credentials = dict(self.credentials(), **{missing: value})
                    terminal = {'type': 'result', 'payload': {'status': 'success', 'credential': credentials}}
                    response = ChunkResponse([json.dumps(terminal).encode()])
                    api = self.api()
                    with patch.object(worker.NO_REDIRECT_OPENER, 'open', return_value=response), self.assertRaisesRegex(
                        worker.WorkerError, '^Session Studio returned incomplete OAuth credentials$'
                    ):
                        worker.process_password_claim(api, self.claim())
                    api.credentials.assert_not_called()
    def test_legacy_json_results_keep_existing_validation(self):
        for status in (None, 'active', 'ok', 'success', 'succeeded'):
            with self.subTest(status=status):
                result = {'credential': self.credentials(), 'error': None}
                if status is not None:
                    result['status'] = status
                api = self.api()
                with patch.object(worker.NO_REDIRECT_OPENER, 'open', return_value=ChunkResponse([json.dumps(result).encode()])):
                    worker.process_password_claim(api, self.claim())
                api.credentials.assert_called_once_with(7, self.credentials(), {})
    def test_malformed_terminal_payload_is_private_and_never_skipped(self):
        for payload in (None, 'synthetic-private', ['synthetic-private']):
            with self.subTest(payload=payload):
                terminal = {'type': 'result', 'payload': payload}
                success = {'type': 'result', 'payload': {'credential': self.credentials()}}
                response = ChunkResponse([json.dumps(terminal).encode() + b'\n' + json.dumps(success).encode()])
                api = self.api()
                with patch.object(worker.NO_REDIRECT_OPENER, 'open', return_value=response), self.assertRaisesRegex(
                    worker.WorkerError, '^Session Studio returned an invalid result$'
                ):
                    worker.process_password_claim(api, self.claim())
                api.credentials.assert_not_called()
    def test_unknown_typed_event_is_private_and_never_skipped(self):
        event = {'type': 'synthetic-private', 'error': 'synthetic-private'}
        success = {'type': 'result', 'payload': {'credential': self.credentials()}}
        api = self.api()
        response = ChunkResponse([json.dumps(event).encode() + b'\n' + json.dumps(success).encode()])
        with patch.object(worker.NO_REDIRECT_OPENER, 'open', return_value=response), self.assertRaisesRegex(
            worker.WorkerError, '^Session Studio returned an unsupported event$'
        ):
            worker.process_password_claim(api, self.claim())
        api.credentials.assert_not_called()
    def test_progress_without_terminal_result_fails_closed(self):
        for event in ({'type': 'progress', 'error': None}, {'status': 'active'}, {'error': None}):
            with self.subTest(event=event), self.assertRaisesRegex(
                worker.WorkerError, '^Session Studio response ended without a result$'
            ):
                worker._session_studio_result(ChunkResponse([json.dumps(event).encode()]))
    def test_invalid_and_unbounded_results_fail_closed(self):
        for data in (b'[]',b'{bad',b'{"type":"result","payload":null}',b'{"type":"heartbeat"}'):
            with self.subTest(data=data),self.assertRaises(worker.WorkerError):
                worker._session_studio_result(ChunkResponse([data]))
        with patch.object(worker,'MAX_RESPONSE_BYTES',8),self.assertRaisesRegex(worker.WorkerError,'too large'):
            worker._session_studio_result(ChunkResponse([b' '*65]))
    def test_session_engine_request_and_cas_submission(self):
        api=self.api();response=ChunkResponse([json.dumps({'credential':self.credentials()}).encode()])
        with patch.dict(os.environ,{'OPENAI_REAUTH_WORKER_TOKEN':'never-forward'}),patch.object(worker.NO_REDIRECT_OPENER,'open',return_value=response) as opener,patch.object(worker.subprocess,'run') as local:
            worker.process_password_claim(api,self.claim())
        req=opener.call_args.args[0]
        self.assertEqual(json.loads(req.data),dict(action='start',auth_mode='password_2fa',email='demo@example.com',password='synthetic-password',mfa_secret='synthetic-totp'))
        self.assertNotIn('never-forward',str(req.headers))
        self.assertNotIn('{{uuid}}',str(req.headers))
        self.assertEqual(req.get_header('Origin'),'https://session.example')
        api.credentials.assert_called_once_with(7,self.credentials(),{})
        local.assert_not_called();self.assertTrue(response.closed)
        self.assertEqual([c.args[1] for c in api.progress.call_args_list],['starting','protocol_connecting','applying_credentials'])
    def test_missing_endpoint_or_invalid_engine_never_falls_back(self):
        for overrides in ({'relogin_endpoint':''},{'relogin_endpoint':'http://session.example/'},{'relogin_endpoint':'https://user:pass@session.example/'},{'relogin_endpoint':'https://session.example/?token=secret'},{'engine':'typo'},{'proxy_url':'http://private-proxy/'},{'relogin_headers':{'X-OpenAI-Reauth-Worker-Token':'secret'}}):
            with self.subTest(overrides=overrides),patch.object(worker.NO_REDIRECT_OPENER,'open') as opener,patch.object(worker.subprocess,'run') as local:
                with self.assertRaises(worker.WorkerError):
                    worker.process_password_claim(self.api(),dict(self.claim(),**overrides))
                opener.assert_not_called();local.assert_not_called()
    def test_session_engine_receives_exact_workspace_without_forwarding_internal_claim(self):
        claim = dict(self.claim(), expected_workspace='workspace-original',
                     expected_credentials_hash='internal-snapshot-not-for-provider',
                     oauth_profile='codex')
        api = self.api()
        response = ChunkResponse([json.dumps({'credential': self.credentials()}).encode()])
        with patch.object(worker.NO_REDIRECT_OPENER, 'open', return_value=response) as opener:
            worker.process_password_claim(api, claim)
        request = opener.call_args.args[0]
        payload = json.loads(request.data)
        self.assertEqual(payload['expected_workspace'], 'workspace-original')
        self.assertEqual(set(payload), {'action', 'auth_mode', 'email', 'password', 'mfa_secret', 'expected_workspace'})
        self.assertNotIn('internal-snapshot-not-for-provider', request.data.decode())
        opener.assert_called_once()
        api.credentials.assert_called_once_with(7, self.credentials(), {})
    def test_provider_failures_do_not_expose_secrets_or_switch_engine(self):
        for response in ({'error':{'code':'synthetic-password','message':'synthetic-totp'}},{'credential':{'access_token':'synthetic-access'}}):
            api=self.api()
            with patch.object(worker.NO_REDIRECT_OPENER,'open',return_value=ChunkResponse([json.dumps(response).encode()])),patch.object(worker.subprocess,'run') as local:
                with self.assertRaises(worker.WorkerError) as failure: worker.process_password_claim(api,self.claim())
                self.assertNotIn('synthetic',str(failure.exception));local.assert_not_called();api.credentials.assert_not_called()
    def test_redirect_response_not_followed(self):
        with patch.object(worker.NO_REDIRECT_OPENER,'open',side_effect=HTTPError('https://session.example/',302,'secret',{},io.BytesIO(b'secret'))):
            with self.assertRaisesRegex(worker.WorkerError,'^Session Studio HTTP 302$'):
                worker.process_password_claim(self.api(),self.claim())
        handler=worker._NoRedirect()
        self.assertIsNone(handler.redirect_request(None,None,302,'',{},'https://other.example/'))
    def test_rejected_cas_is_not_reported_successful(self):
        api=self.api();api.credentials.return_value={'status':'failed','error':'synthetic-secret'}
        with patch.object(worker.NO_REDIRECT_OPENER,'open',return_value=ChunkResponse([json.dumps({'credential':self.credentials()}).encode()])):
            with self.assertRaisesRegex(worker.WorkerError,'credentials were not accepted'):
                worker.process_password_claim(api,self.claim())
    def test_config_concurrency_bounds(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);(root/'src').mkdir()
            for name in ('protocol-login.mjs','tls-transport.mjs'):(root/'src'/name).touch()
            base=dict(OPENAI_REAUTH_WORKER_TOKEN='x'*32,TOSUB2_ROOT=directory)
            for val in ('0','17','-1','2.5','NaN'):
                with patch.dict(os.environ,dict(base,OPENAI_REAUTH_CONCURRENCY=val),clear=True),self.assertRaises(worker.WorkerError):worker.WorkerConfig.from_env()
            for val in ('1','3','16'):
                with patch.dict(os.environ,dict(base,OPENAI_REAUTH_CONCURRENCY=val),clear=True):self.assertEqual(worker.WorkerConfig.from_env().concurrency,int(val))
    def test_slot_ids_are_distinct_and_bounded(self):
        cfg=worker.WorkerConfig('http://127.0.0.1','x'*32,'x'*128,None)
        ids=[worker.worker_slot_config(cfg,i).worker_id for i in range(1,17)]
        self.assertEqual(len(set(ids)),16);self.assertTrue(all(len(i)<=128 for i in ids))
    @unittest.skipUnless(sys.platform.startswith('linux'),'fork integration')
    def test_three_real_worker_processes_overlap(self):
        with tempfile.TemporaryDirectory() as directory:
            cfg=worker.WorkerConfig(directory,'x'*32,'probe',None,concurrency=3)
            with patch.object(worker.WorkerAPI,'runtime_concurrency',return_value=3),patch.object(worker,'_worker_loop',parallel_probe),self.assertRaisesRegex(worker.WorkerError,'process exited'):
                worker.run_worker_pool(cfg)
            self.assertEqual(len(list(Path(directory).glob('done-*'))),3)
    def test_once_does_not_start_a_pool(self):
        cfg=worker.WorkerConfig('http://127.0.0.1','x'*32,'test',None,concurrency=3)
        with patch('sys.argv',['worker','--once']),patch.object(worker.WorkerConfig,'from_env',return_value=cfg),patch.object(worker.shutil,'which',return_value='node'),patch.object(worker,'run_once',return_value=False) as run,patch.object(worker,'run_worker_pool') as pool:
            self.assertEqual(worker.main(),0);run.assert_called_once();pool.assert_not_called()


if __name__=='__main__':unittest.main()
