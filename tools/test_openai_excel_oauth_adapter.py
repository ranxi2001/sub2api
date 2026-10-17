import base64
import json
import os
import subprocess
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import patch

from tools import openai_excel_oauth_adapter as adapter


def token(payload):
    b64 = base64.urlsafe_b64encode(json.dumps(payload).encode()).decode().rstrip('=')
    return 'e30.' + b64 + '.test'


class ExcelAdapterTests(unittest.TestCase):
    def credentials(self):
        return {'client_id': adapter.CLIENT_ID, 'chatgpt_account_id': 'workspace-a',
                'refresh_token': 'synthetic-refresh',
                'access_token': token({'client_id': adapter.CLIENT_ID, 'exp': int(time.time())+3600,
                                      'https://api.openai.com/auth': {'chatgpt_account_id': 'workspace-a'}}),
                'id_token': token({'aud': adapter.CLIENT_ID, 'email': 'test@example.invalid'})}

    def test_matching_excel_session_accepted(self):
        adapter.validate_excel_credentials(self.credentials(), 'test@example.invalid', 'workspace-a')

    def test_wrong_client_email_and_workspace_rejected(self):
        for changes,email,workspace in [({'client_id':'codex'},'test@example.invalid','workspace-a'),
                                        ({},'someone@example.invalid','workspace-a'),
                                        ({},'test@example.invalid','workspace-b'),
                                        ({'id_token':token({'aud':'codex','email':'test@example.invalid'})},'test@example.invalid','workspace-a'),
                                        ({'refresh_token':''},'test@example.invalid','workspace-a')]:
            with self.subTest(changes=changes),self.assertRaises(ValueError):
                adapter.validate_excel_credentials({**self.credentials(),**changes},email,workspace)

    def test_unknown_runtime_fails_before_creating_or_modifying_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)/'source';(root/'src').mkdir(parents=True)
            script=root/'src/protocol-login.mjs';script.write_text('// changed runtime')
            output=Path(directory)/'temp';output.mkdir()
            with self.assertRaises(ValueError):adapter.prepare_excel_runtime(root,output)
            self.assertEqual(script.read_text(),'// changed runtime')
            self.assertEqual(list(output.iterdir()),[])

    def test_staging_keeps_shared_runtime_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)/'source';(root/'src').mkdir(parents=True);(root/'node_modules').mkdir()
            script=root/'src/protocol-login.mjs';script.write_text('original')
            output=Path(directory)/'temp';output.mkdir()
            with patch.object(adapter,'excel_source',return_value='excel'):
                staged=adapter.prepare_excel_runtime(root,output)
            self.assertEqual(staged.read_text(),'excel');self.assertEqual(script.read_text(),'original')
            self.assertEqual((staged.parent.parent/'node_modules').resolve(),root/'node_modules')

    def test_contract_change_never_silently_partially_patches(self):
        with self.assertRaises(ValueError):adapter.replace_exact('old old','old','new')
        self.assertEqual(adapter.replace_exact('old old','old','new',count=2),'new new')

    @unittest.skipUnless(os.getenv('EXCEL_TEST_TOSUB2_ROOT'), 'set EXCEL_TEST_TOSUB2_ROOT to the pinned dependency')
    def test_pinned_dependency_transforms_and_parses_without_mutating_original(self):
        root=Path(os.environ['EXCEL_TEST_TOSUB2_ROOT'])
        original=(root/'src/protocol-login.mjs').read_bytes()
        with tempfile.TemporaryDirectory() as d:
            staged=adapter.prepare_excel_runtime(root,Path(d))
            subprocess.run(['node','--check',str(staged)],check=True,capture_output=True)
        self.assertEqual((root/'src/protocol-login.mjs').read_bytes(),original)


@unittest.skipUnless(os.getenv('EXCEL_TEST_TOSUB2_ROOT'), 'set EXCEL_TEST_TOSUB2_ROOT to the pinned dependency')
class ExcelPreflightTests(unittest.TestCase):
    """Execute the pinned CLI with an offline transport, not the real service."""

    def run_login(self, *, excel=True, reject_auth=False):
        dependency = Path(os.environ['EXCEL_TEST_TOSUB2_ROOT'])
        original = (dependency / 'src/protocol-login.mjs').read_bytes()
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            if excel:
                script = adapter.prepare_excel_runtime(dependency, temporary)
            else:
                script = temporary / 'protocol-login.mjs'
                script.write_bytes(original)
            # The CLI's only non-builtin import is its TLS transport (plus the
            # staged Excel flow). Every possible network request stops here.
            script.with_name('tls-transport.mjs').write_text('''
export function browserIdentityForTlsProfile() {
  return {userAgent: 'Offline fixture', acceptLanguage: 'en-US'};
}
export function shouldUseTlsTransport() { return true; }
function record(event) { console.log('FIXTURE ' + JSON.stringify(event)); }
export class TlsFingerprintTransport {
  constructor() { this.enabled = true; this.profile = 'chrome146'; }
  async prepareProxy(proxy, url) {
    record({event: 'preflight', proxy, url});
    if (new URL(url).hostname === 'chatgpt.com') {
      throw new Error('MOCK_CHATGPT_HOMEPAGE_403');
    }
    if (process.env.EXCEL_FIXTURE_REJECT_AUTH === '1') {
      throw new Error('MOCK_AUTH_PREFLIGHT_403');
    }
  }
  async fetch(url) {
    const target = new URL(url);
    record({event: 'fetch', origin: target.origin, path: target.pathname});
    if (target.origin === 'https://auth.openai.com' &&
        target.pathname === '/api/accounts/authorize') {
      throw new Error('MOCK_EXCEL_AUTHORIZATION_REACHED');
    }
    throw new Error('MOCK_UNEXPECTED_NETWORK_REQUEST');
  }
  async close() { record({event: 'closed'}); }
}
''')
            env = {k: v for k, v in os.environ.items() if not k.startswith(
                ('CHATGPT_', 'AUTH_BASE', 'TOSUB2_', 'OPENAI_EXCEL_', 'EXCEL_FIXTURE_'))}
            env.update(CHATGPT_LOGIN_PASSWORD='synthetic-password',
                       CHATGPT_TOTP_SECRET='synthetic-totp',
                       EXCEL_FIXTURE_REJECT_AUTH='1' if reject_auth else '0')
            result = subprocess.run(
                ['node', str(script), '--email', 'test@example.invalid',
                 '--proxy', 'http://127.0.0.1:18080', '--output-mode', 'sub2api',
                 '--sub2api-out', str(temporary / 'account.json')],
                cwd=temporary, env=env, stdin=subprocess.DEVNULL,
                capture_output=True, text=True, timeout=15)
            events = [json.loads(line.removeprefix('FIXTURE '))
                      for line in result.stdout.splitlines() if line.startswith('FIXTURE ')]
            self.assertFalse((temporary / 'account.json').exists())
        self.assertEqual((dependency / 'src/protocol-login.mjs').read_bytes(), original)
        return result, events

    def test_excel_reaches_pkce_when_chatgpt_homepage_is_forbidden(self):
        result, events = self.run_login()
        self.assertEqual(result.returncode, 1)  # fixture stops before credentials
        self.assertIn('MOCK_EXCEL_AUTHORIZATION_REACHED', result.stderr)
        self.assertEqual(events, [
            {'event': 'preflight', 'proxy': 'http://127.0.0.1:18080/',
             'url': 'https://auth.openai.com/'},
            {'event': 'fetch', 'origin': 'https://auth.openai.com',
             'path': '/api/accounts/authorize'},
            {'event': 'closed'},
        ])

    def test_auth_preflight_failure_stops_without_login_or_retry(self):
        result, events = self.run_login(reject_auth=True)
        self.assertEqual(result.returncode, 1)
        self.assertIn('MOCK_AUTH_PREFLIGHT_403', result.stderr)
        self.assertEqual(events, [
            {'event': 'preflight', 'proxy': 'http://127.0.0.1:18080/',
             'url': 'https://auth.openai.com/'},
            {'event': 'closed'},
        ])

    def test_codex_still_checks_chatgpt_homepage(self):
        result, events = self.run_login(excel=False)
        self.assertEqual(result.returncode, 1)
        self.assertIn('MOCK_CHATGPT_HOMEPAGE_403', result.stderr)
        self.assertEqual(events, [
            {'event': 'preflight', 'proxy': 'http://127.0.0.1:18080/',
             'url': 'https://chatgpt.com/'},
            {'event': 'closed'},
        ])


if __name__=='__main__':unittest.main()
