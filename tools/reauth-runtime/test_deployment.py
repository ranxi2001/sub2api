"""Local deployed API/PostgreSQL and packaged Worker integration; upstream MFA is mocked."""
import json
import os
from pathlib import Path
import subprocess
import sys
import urllib.error
import urllib.request
from unittest.mock import patch

ROOT = Path(os.environ['STAGING_ROOT']).resolve()
CURRENT = ROOT / 'current'
BASE = os.environ['STAGING_API_URL'].rstrip('/') + '/api/v1'
from urllib.parse import urlparse
assert urlparse(BASE).hostname in ('127.0.0.1', 'localhost', '::1'), 'Use an isolated local API'
OLD = 'JBSWY3DPEHPK3PXP'
NEW = 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ'
WORKER_TOKEN = os.environ['OPENAI_REAUTH_WORKER_TOKEN']
TOKEN = ''

def request(method, path, body=None):
    headers = {'Content-Type': 'application/json'}
    if TOKEN:
        headers['Authorization'] = 'Bearer ' + TOKEN
    req = urllib.request.Request(BASE + path, data=json.dumps(body).encode() if body is not None else None, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=30) as response:
            envelope = json.load(response)
            assert envelope['code'] == 0, envelope
            return envelope['data']
    except urllib.error.HTTPError as error:
        body = json.load(error)
        raise RuntimeError(str(error.code) + ' ' + json.dumps(body)) from None

login = request('POST', '/auth/login', {'email': os.environ['STAGING_ADMIN_EMAIL'], 'password': os.environ['STAGING_ADMIN_PASSWORD']})
TOKEN = login['access_token']
status = request('GET', '/admin/compliance')
request('POST', '/admin/compliance/accept', {'phrase': status['ack_phrase_en'], 'language': 'en'})
request('POST', '/admin/account-ops/token-guard-v2/encryption/initialize', {})
account = request('POST', '/admin/accounts', {'name': '2FA staging synthetic', 'platform': 'openai', 'type': 'oauth', 'credentials': {'email': 'rotation@example.test', 'access_token': 'synthetic-token'}, 'extra': {'email': 'rotation@example.test'}, 'concurrency': 1, 'priority': 50})
ID = account['id']
request('PUT', '/admin/account-ops/token-guard-v2/accounts/' + str(ID), {'login_email': 'rotation@example.test', 'credential_mode': 'password_totp', 'engine': 'session_studio', 'proxy_source': 'account', 'password': 'synthetic-password', 'totp_secret': OLD, 'enabled': False, 'auto_relogin_enabled': False})
os.environ.update(OPENAI_REAUTH_WORKER_TOKEN=WORKER_TOKEN, SUB2API_BASE_URL=BASE.removesuffix('/api/v1'),
    TOSUB2_ROOT=str(CURRENT / 'tosub2'), NODE_EXECUTABLE=str(CURRENT / 'node'), TOSUB2_PYTHON=str(CURRENT / 'python'),
    OPENAI_TOTP_JOURNAL_DIR=str(ROOT / 'totp-recovery'), PYTHONDONTWRITEBYTECODE='1')
subprocess.run([str(CURRENT / 'python'), str(CURRENT / 'worker.py'), '--check'], check=True, env=os.environ)
subprocess.run([str(CURRENT / 'python'), str(CURRENT / 'worker.py'), '--once'], check=True, env=os.environ)
print('Packaged Worker through current symlink: configuration and real API heartbeat passed.', flush=True)

sys.path.insert(0, str(CURRENT))
from worker import WorkerAPI, WorkerConfig
from openai_totp_rotation import Journal, RotationError, rotate
config = WorkerConfig.from_env()
api = WorkerAPI(config)
journal = Journal()

task = request('POST', f'/admin/accounts/{ID}/totp-rotation', {})
claim = api.claim_totp()
assert claim['task_id'] == task['task_id'] and claim['account_id'] == ID
phases = []

class MockMFA:
    def request(self, method, route, payload=None):
        if route.endswith('/enroll'):
            phases.append('enroll')
            return {'secret': NEW, 'session_id': 'synthetic-enrollment', 'factor_id': 'F2'}
        assert route.endswith('/activate_enrollment')
        saved = next(x for x in journal.call({'action': 'list'}) if x['task_id'] == task['task_id'])
        assert saved['candidate'] == NEW and saved['factor_id'] == 'F2' and saved['database_acknowledged']
        status = request('GET', f'/admin/accounts/{ID}/totp-rotation')
        assert status['state'] == 'activating' and status['has_candidate']
        phases.append('activate-after-local-and-database-save')
        return {'success': True}

    def close(self):
        pass

def mock_login(_config, _claim, secret):
    assert _claim['totp_factor_id'] == 'F2'
    phases.append('verify-new')
    # The old factor remains valid. Success verifies the selected factor only.
    return {'web': {'mfaVerified': True, 'mfaFactorId': 'F2'}}

with patch('openai_totp_rotation.time.sleep'):
    rotate(api, claim, session_factory=lambda *_: MockMFA(), login=mock_login)
status = request('GET', f'/admin/accounts/{ID}/totp-rotation')
assert status['state'] == 'succeeded' and status['outcome'] == 'selected_factor_verified_no_revocation', status
exported = request('POST', f'/admin/accounts/{ID}/totp-export', {'source': 'current', 'include_password': False})
assert exported['secret'] == NEW and exported['factor_id'] == 'F2' and 'password' not in exported
assert phases == ['enroll', 'activate-after-local-and-database-save', 'verify-new'], phases
print('Real API/PostgreSQL rotation: succeeded; export and encrypted configuration updated.', flush=True)

task2 = request('POST', f'/admin/accounts/{ID}/totp-rotation', {})
claim2 = api.claim_totp()
candidate2 = 'MFRGGZDFMZTWQ2LKNNWG23TPOI'

class TimeoutMFA:
    def request(self, method, route, payload=None):
        if route.endswith('/enroll'):
            return {'secret': candidate2, 'session_id': 'synthetic-timeout', 'factor_id': 'F3'}
        raise TimeoutError('synthetic activation response loss')
    def close(self):
        pass

rotate(api, claim2, session_factory=lambda *_: TimeoutMFA(), login=mock_login)
status = request('GET', f'/admin/accounts/{ID}/totp-rotation')
assert status['state'] == 'uncertain' and status['has_candidate'], status
try:
    request('POST', f'/admin/accounts/{ID}/totp-rotation', {})
except RuntimeError as error:
    assert str(error).startswith('409 '), error
else:
    raise AssertionError('uncertain task must block a new rotation')
restored = Journal().call({'action': 'list'})
assert any(x['task_id'] == task2['task_id'] and x['candidate'] == candidate2 for x in restored)
print('Activation timeout: uncertain, new rotation blocked, candidate survives journal reopening.', flush=True)
request('POST', f'/admin/accounts/{ID}/totp-rotation/verify', {'task_id': task2['task_id'], 'action': 'verify_new'})
recovery = api.claim_totp()
assert recovery['action'] == 'verify_new' and recovery['candidate_secret'] == candidate2 and recovery['candidate_factor_id'] == 'F3'
with patch('openai_totp_rotation.time.sleep'):
    rotate(api, recovery, session_factory=lambda *_: (_ for _ in ()).throw(AssertionError('Recovery must not enroll')),
           login=lambda _config, _claim, secret: {'web': {'mfaVerified': True, 'mfaFactorId': 'F3'}} if secret == candidate2 and _claim['totp_factor_id'] == 'F3' else None)
assert request('GET', f'/admin/accounts/{ID}/totp-rotation')['state'] == 'succeeded'
print('Recovery verifies the saved candidate and completes without another enrollment.', flush=True)
# An unresolved task retains its own identity/credential snapshot after soft delete.
task3 = request('POST', f'/admin/accounts/{ID}/totp-rotation', {})
claim3 = api.claim_totp()
class DeletedTimeoutMFA:
    def request(self, method, route, payload=None):
        if route.endswith('/enroll'):
            return {'secret': OLD, 'session_id': 'deleted-enrollment', 'factor_id': 'F4'}
        raise TimeoutError('synthetic activation response loss')
    def close(self): pass
rotate(api, claim3, session_factory=lambda *_: DeletedTimeoutMFA(), login=mock_login)
assert request('GET', f'/admin/accounts/{ID}/totp-rotation')['state'] == 'uncertain'
request('DELETE', f'/admin/accounts/{ID}')
recovery_request = {'task_id': task3['task_id'], 'expected_email': 'rotation@example.test', 'source': 'candidate'}
restored = request('POST', '/admin/accounts/totp-recovery-export', recovery_request)
assert restored['secret'] == OLD and restored['factor_id'] == 'F4' and 'password' not in restored
try:
    request('POST', '/admin/accounts/totp-recovery-export', {**recovery_request, 'expected_email': 'wrong@example.test'})
except RuntimeError as error:
    assert str(error).startswith('409 ')
else:
    raise AssertionError('wrong recovery identity must fail')
destination = request('POST', '/admin/accounts', {'name': 'Recovered synthetic', 'platform': 'openai', 'type': 'oauth', 'credentials': {'email': 'rotation@example.test', 'access_token': 'synthetic-token'}, 'extra': {'email': 'rotation@example.test'}, 'concurrency': 1, 'priority': 50})
new_id = destination['id']
result = request('POST', f'/admin/accounts/{new_id}/totp-recovery-import', recovery_request)
assert result['imported'] and result['verified'] is False and result['engine'] == 'local_worker'
try:
    request('POST', f'/admin/accounts/{new_id}/totp-rotation', {})
except RuntimeError as error:
    assert str(error).startswith('409 ')
else:
    raise AssertionError('reimport must not reenroll an unresolved identity')
normal_task = request('POST', f'/admin/accounts/{new_id}/openai-reauth', {})
normal_claim = api.claim()
assert normal_claim['task_id'] == normal_task['id'] and normal_claim['account_id'] == new_id
assert normal_claim['totp_secret'] == OLD and normal_claim['totp_factor_id'] == 'F4'
api.fail(normal_claim['task_id'], 'worker_failed')  # No real authentication is performed.
print('Soft-delete recovery: task snapshot exported, matching new account imported, normal claim bound; reenrollment blocked.', flush=True)
report = {'account_id': ID, 'successful_task': task['task_id'], 'uncertain_task': task2['task_id'], 'deleted_task': task3['task_id'], 'reimport_account': new_id,
          'checks': ['packaged-worker-current-symlink', 'real-api-heartbeat', 'postgres-task-creation-and-transitions', 'paired-dual-save-before-activation', 'bound-configuration-update-and-export', 'activation-timeout-and-restart-recovery', 'verify-new-without-reenrollment', 'soft-deleted-task-snapshot-export', 'identity-checked-recovery-import', 'normal-worker-claim-retains-binding', 'reimport-does-not-reenroll'],
          'upstream': 'mock', 'production_changes': False, 'real_login_2fa': 'NOT EXECUTED'}
(ROOT / 'verification.json').write_text(json.dumps(report, indent=2) + '\n')
print(json.dumps(report, indent=2))
