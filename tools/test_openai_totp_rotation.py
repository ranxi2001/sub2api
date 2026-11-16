"""Offline failure injection: no real accounts, tokens, or outbound HTTP."""
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import Mock, patch

from tools.openai_oauth_reauth_worker import _extract_chatgpt_session_token
from tools.openai_totp_rotation import RotationError, login_web, rotate, totp, verify_pair

OLD = 'JBSWY3DPEHPK3PXP'
NEW = 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ'


class RotationTests(unittest.TestCase):
    def setUp(self):
        wait = patch('tools.openai_totp_rotation.time.sleep')
        wait.start()
        self.addCleanup(wait.stop)
        self.events = []
        self.claim = dict(task_id=7, account_id=42, login_email='owner@example.test', password='synthetic-password', totp_secret=OLD)
        self.api = Mock(config=SimpleNamespace(tosub2_root=Path('/fake')))
        self.api.totp_phase.side_effect = lambda task, phase, *args: self.events.append(phase)
        self.api.totp_finish.side_effect = lambda *args: self.events.append(('finish', args))
        self.journal = Mock()
        self.journal.save.side_effect = lambda *args: self.events.append('journal_candidate' if len(args) > 1 else 'journal_old')
        self.session = Mock()
        def request(method, path, payload):
            self.events.append('activate' if path.endswith('activate_enrollment') else 'enroll')
            return {'success': True} if path.endswith('activate_enrollment') else {'secret': NEW, 'session_id': 'fake-session'}
        self.session.request.side_effect = request
        self.login = Mock(side_effect=[{'cookies': []}, None])

    def run_rotation(self):
        rotate(self.api, self.claim, session_factory=lambda *a: self.session, login=self.login, journal_factory=lambda: self.journal)

    def test_activation_requires_both_durable_copies_and_both_verifications(self):
        self.run_rotation()
        self.assertLess(self.events.index('journal_candidate'), self.events.index('prepared'))
        self.assertLess(self.events.index('prepared'), self.events.index('activate'))
        self.api.totp_finish.assert_called_once_with(7, True)
        self.assertEqual([call.args[2] for call in self.login.call_args_list], [NEW, OLD])

    def test_local_write_failure_never_activates(self):
        self.journal.save.side_effect = [None, OSError('disk full')]
        self.run_rotation()
        self.assertEqual(self.session.request.call_count, 1)
        self.api.totp_finish.assert_called_once_with(7, False, 'enrollment_failed')

    def test_database_ack_failure_never_activates_or_drops_local_candidate(self):
        def phase(task, stage, *args):
            if stage == 'prepared': raise TimeoutError('lost ack')
        self.api.totp_phase.side_effect = phase
        self.run_rotation()
        self.assertEqual(self.session.request.call_count, 1)
        self.assertEqual(self.journal.save.call_args.args[1], NEW)
        self.assertFalse(self.api.totp_finish.call_args.args[1])

    def test_activation_timeout_does_not_reenroll_and_keeps_candidate(self):
        self.session.request.side_effect = [{'secret': NEW, 'session_id': 'fake'}, TimeoutError('ambiguous')]
        self.run_rotation()
        self.assertEqual(self.session.request.call_count, 2)
        self.assertEqual(self.journal.save.call_args.args[1], NEW)
        self.api.totp_finish.assert_called_once_with(7, False, 'activation_unconfirmed')
        self.login.assert_not_called()

    def test_two_valid_factors_preserve_candidate_with_specific_diagnostic(self):
        self.login.side_effect = [{}, {}]
        self.run_rotation()
        self.api.totp_finish.assert_called_once_with(7, False, 'new_accepted_old_valid')
        self.assertEqual(self.journal.save.call_args.args[1], NEW)
        self.assertEqual(self.session.request.call_count, 2)
        self.assertEqual([call.args[2] for call in self.login.call_args_list], [NEW, OLD])

    def test_candidate_rejected_remains_verification_failed(self):
        self.login.side_effect = [None]
        self.run_rotation()
        self.api.totp_finish.assert_called_once_with(7, False, 'verification_failed')
        self.assertEqual(self.login.call_count, 1)

    def test_old_recovery_distinguishes_two_valid_factors(self):
        self.claim.update(action='verify_old', candidate_secret=NEW)
        self.login.side_effect = [{}, {}]
        self.run_rotation()
        self.api.totp_finish.assert_called_once_with(7, False, 'old_accepted_new_valid')
        self.session.request.assert_not_called()

    def test_network_error_is_not_evidence_old_secret_was_rejected(self):
        self.login.side_effect = [{}, RotationError('login_failed')]
        self.run_rotation()
        self.api.totp_finish.assert_called_once_with(7, False, 'verification_failed')

    def test_recovery_only_verifies_saved_candidate(self):
        self.claim.update(action='verify_new', candidate_secret=NEW)
        self.run_rotation()
        self.session.request.assert_not_called()
        self.journal.save.assert_not_called()
        self.api.totp_phase.assert_called_once_with(7, 'verify_recovery')
        self.api.totp_finish.assert_called_once_with(7, True)

    def test_old_recovery_requires_new_secret_rejection(self):
        self.claim.update(action='verify_old', candidate_secret=NEW)
        self.run_rotation()
        self.assertEqual([call.args[2] for call in self.login.call_args_list], [OLD, NEW])
        self.api.totp_finish.assert_called_once_with(7, True)

    def test_missing_journal_prevents_all_upstream_calls(self):
        rotate(self.api, self.claim, session_factory=self.session,
               journal_factory=Mock(side_effect=OSError('disk unavailable')))
        self.session.assert_not_called()

    def test_exception_never_enters_report(self):
        self.session.request.side_effect = RuntimeError(NEW + self.claim['password'])
        self.run_rotation()
        self.assertNotIn(NEW, str(self.api.totp_finish.call_args))
        self.assertNotIn(self.claim['password'], str(self.api.totp_finish.call_args))

    def test_totp_matches_rfc6238(self):
        self.assertEqual(totp(NEW, 59), '287082')

    def test_runner_missing_mfa_is_not_valid_verification(self):
        def runner(command, **kwargs):
            dest = Path(command[command.index('--out')+1])
            dest.write_text(json.dumps({'web': {'mfaVerified': False}, 'cookies': []}))
            return SimpleNamespace(returncode=0, stdout='', stderr='')
        with patch('tools.openai_totp_rotation.subprocess.run', runner):
            with self.assertRaisesRegex(RotationError, 'verification_failed'):
                login_web(self.api.config, self.claim, OLD)

    def test_runner_password_rejection_has_a_specific_error_code(self):
        with patch('tools.openai_totp_rotation.subprocess.run', return_value=SimpleNamespace(
            returncode=1, stdout='[warn] Password was rejected. Enter it again.', stderr='')):
            with self.assertRaisesRegex(RotationError, 'password_rejected'):
                login_web(self.api.config, self.claim, OLD)

    def test_runner_proxy_failure_has_a_specific_error_code(self):
        claim = dict(self.claim, proxy_url='socks5h://127.0.0.1:7890')
        with patch('tools.openai_totp_rotation.subprocess.run', return_value=SimpleNamespace(
            returncode=1, stdout='', stderr='connect ECONNREFUSED 127.0.0.1:7890')):
            with self.assertRaisesRegex(RotationError, 'proxy_unavailable'):
                login_web(self.api.config, claim, OLD)

    def test_runner_secrets_are_not_process_arguments(self):
        with patch('tools.openai_totp_rotation.subprocess.run', return_value=SimpleNamespace(returncode=1, stdout='', stderr='')) as run:
            with self.assertRaises(RotationError): login_web(self.api.config, self.claim, OLD)
            self.assertNotIn(OLD, str(run.call_args.args))
            self.assertNotIn(self.claim['password'], str(run.call_args.args))
            self.assertNotIn('OPENAI_REAUTH_WORKER_TOKEN', run.call_args.kwargs['env'])

    def test_session_token_extracts_single_cookie(self):
        self.assertEqual(
            _extract_chatgpt_session_token([
                {'name': '__Secure-next-auth.session-token', 'value': 'session-value'},
            ]),
            'session-value',
        )

    def test_session_token_joins_contiguous_chunks(self):
        self.assertEqual(
            _extract_chatgpt_session_token([
                {'name': '__Secure-next-auth.session-token.1', 'value': 'part-b'},
                {'name': '__Secure-next-auth.session-token.0', 'value': 'part-a'},
            ]),
            'part-apart-b',
        )

    def test_session_token_rejects_missing_chunk(self):
        self.assertEqual(
            _extract_chatgpt_session_token([
                {'name': '__Secure-next-auth.session-token.1', 'value': 'part-b'},
            ]),
            '',
        )


if __name__ == '__main__':
    unittest.main()
