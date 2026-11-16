"""Owned TOTP rotation protocol. No hosted credential service or rotation package.

Login reuses the existing bundled runner. MFA requests have no automatic retries.
The candidate is fsync'ed to an encrypted journal AND acknowledged by the API
before activation. Journals survive worker upgrades/restarts and are never auto-deleted.
"""
from __future__ import annotations

import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import tempfile
import time
from typing import Any


class RotationError(Exception):
    def __init__(self, code: str):
        self.code = code
        super().__init__(code)


def _proxy_failure_output(output: str) -> bool:
    text = str(output or "").lower()
    return any(marker in text for marker in (
        "econnrefused", "connection refused", "proxy connect", "proxy error",
        "could not connect", "connect etimedout", "socket hang up",
    ))


def totp(secret: str, at: float | None = None) -> str:
    value = ''.join(secret.upper().split()).rstrip('=')
    if not 16 <= len(value) <= 128:
        raise RotationError('enrollment_failed')
    key = base64.b32decode(value + '=' * (-len(value) % 8))
    counter = int((time.time() if at is None else at) // 30)
    digest = hmac.new(key, struct.pack('>Q', counter), hashlib.sha1).digest()
    offset = digest[-1] & 15
    return f'{(struct.unpack(">I", digest[offset:offset+4])[0] & 0x7fffffff) % 1000000:06d}'


class Journal:
    def __init__(self):
        self.node = os.getenv('NODE_EXECUTABLE', 'node')
        self.root = os.getenv('OPENAI_TOTP_JOURNAL_DIR', '')
        if not self.root or not Path(self.root).is_absolute() or not shutil.which(self.node):
            raise RotationError('journal_unavailable')
        self.call({'action': 'check'})

    def call(self, payload):
        child = subprocess.run([self.node, str(Path(__file__).with_name('openai_totp_journal.mjs'))],
                               input=json.dumps(payload), capture_output=True, text=True, timeout=30,
                               env={**os.environ, 'OPENAI_TOTP_JOURNAL_DIR': self.root}, check=False)
        if child.returncode:
            raise RotationError('journal_unavailable')
        return json.loads(child.stdout)

    def save(self, claim, candidate='', session_id='', factor_id=''):
        self.call({'action': 'write', 'record': {'task_id': claim['task_id'], 'account_id': claim['account_id'],
                   'email': claim['login_email'], 'password': claim['password'], 'previous': claim['totp_secret'],
                   'candidate': candidate, 'session_id': session_id, 'factor_id': factor_id,
                   'previous_factor_id': claim.get('totp_factor_id', '')}})

    def acknowledge(self, task_id):
        self.call({'action': 'acknowledge', 'task_id': task_id})

    def recover(self, api):
        for record in self.call({'action': 'list'}):
            if record.get('candidate') and not record.get('database_acknowledged'):
                # The API accepts this only for an uncertain task with no different candidate.
                try:
                    api.totp_recover(record['task_id'], record['candidate'], record.get('factor_id', ''), record.get('session_id', ''))
                    self.acknowledge(record['task_id'])
                except Exception:
                    pass  # Keep the encrypted journal for a later reconciliation.


def login_web(config, claim, secret):
    """Fresh cookie jar each time; explicit TOTP rejection differs from all other failures."""
    root = config.tosub2_root
    if root is None:
        raise RotationError('login_failed')
    with tempfile.TemporaryDirectory(prefix='sub2api-totp-login-') as folder:
        session_file = Path(folder) / 'web.json'
        child_env = {k: v for k, v in os.environ.items() if k in (
            'PATH', 'LANG', 'SYSTEMROOT', 'TEMP', 'TMP', 'PYTHONHOME', 'LD_LIBRARY_PATH',
            'SSL_CERT_FILE', 'CURL_CA_BUNDLE', 'TOSUB2_PYTHON', 'NODE_EXECUTABLE')}
        child_env.update(CHATGPT_LOGIN_PASSWORD=claim['password'], CHATGPT_TOTP_SECRET=secret,
                         CHATGPT_PROXY_MAX_ATTEMPTS='1', CHATGPT_PROXY_URL=claim.get('proxy_url', ''),
                         CHATGPT_TOTP_FACTOR_ID=claim.get('totp_factor_id', ''))
        command = [os.getenv('NODE_EXECUTABLE', 'node'), str(root / 'src/protocol-login.mjs'),
                   '--email', claim['login_email'], '--web-only', '--output-mode', 'session', '--out', str(session_file)]
        try:
            result = subprocess.run(command, cwd=folder, env=child_env, stdin=subprocess.DEVNULL,
                                    capture_output=True, text=True, timeout=180, check=False)
        except (OSError, subprocess.TimeoutExpired):
            raise RotationError('login_failed') from None
        # Raw runner output never enters the worker's logger or task errors.
        output = result.stdout + '\n' + result.stderr
        if 'The code generated from the configured 2FA key was rejected' in output:
            return None
        if 'TOTP_FACTOR_' in output:
            raise RotationError('factor_binding_unconfirmed')
        if 'Password was rejected' in output:
            raise RotationError('password_rejected')
        if result.returncode or not session_file.exists():
            if claim.get('proxy_url') and _proxy_failure_output(output):
                raise RotationError('proxy_unavailable')
            raise RotationError('login_failed')
        data = json.loads(session_file.read_text())
        web = data.get('web', {})
        if web.get('mfaVerified') is not True:
            raise RotationError('verification_failed')
        if not valid_factor_id(web.get('mfaFactorId')) or (claim.get('totp_factor_id') and web['mfaFactorId'] != claim['totp_factor_id']):
            raise RotationError('factor_binding_unconfirmed')
        return data


class WebSession:
    def __init__(self, config, claim, secret, login=login_web):
        data = login(config, claim, secret)
        if data is None:
            raise RotationError('totp_rejected')
        from curl_cffi.requests import Session
        self.client = Session(impersonate='chrome146', trust_env=False, proxy=claim.get('proxy_url') or None)
        for cookie in data.get('cookies', []):
            domain = str(cookie.get('domain', '')).lstrip('.')
            if domain in ('chatgpt.com', 'auth.openai.com', 'openai.com'):
                self.client.cookies.set(cookie['name'], cookie['value'], domain=cookie['domain'], path=cookie.get('path', '/'))
        try:
            payload = self.request('GET', '/api/auth/session')
            # Keep the complete upstream Session JSON.  Session export uses
            # this exact authenticated response; TOTP rotation only needs the
            # access token below to authorize its follow-up requests.
            self.auth_session_payload = payload
            if str(payload.get('user', {}).get('email', '')).lower() != claim['login_email'].lower():
                raise RotationError('identity_mismatch')
            token = payload.get('accessToken')
            if not isinstance(token, str) or not token:
                raise RotationError('login_failed')
            self.client.headers.update({'Authorization': 'Bearer ' + token, 'Origin': 'https://chatgpt.com',
                                        'Referer': 'https://chatgpt.com/', 'oai-device-id': data.get('web', {}).get('deviceId', '')})
        except Exception:
            self.close()
            raise

    def request(self, method, route, payload=None):
        try:
            response = self.client.request(method, 'https://chatgpt.com' + route, json=payload,
                                           timeout=30, allow_redirects=False)
            if response.status_code != 200:
                raise RotationError('upstream_rejected')
            data = response.json()
            if not isinstance(data, dict):
                raise RotationError('invalid_response')
            return data
        except RotationError:
            raise
        except Exception:
            raise RotationError('upstream_unavailable') from None

    def close(self):
        self.client.close()


def verify_pair(config, claim, candidate, action, login):
    """Verify the selected credential; adding a factor does not revoke another."""
    old = action == 'verify_old'
    secret = claim['totp_secret'] if old else candidate
    factor = claim.get('totp_factor_id' if old else 'candidate_factor_id', '')
    if not secret:
        raise RotationError('verification_failed')
    data = login(config, {**claim, 'totp_factor_id': factor}, secret)
    if data is None:
        raise RotationError('verification_failed')
    web = data.get('web', {})
    verified = web.get('mfaFactorId')
    if web.get('mfaVerified') is not True or not valid_factor_id(verified) or (factor and verified != factor):
        raise RotationError('factor_binding_unconfirmed')
    return secret, verified


def valid_factor_id(value):
    import re
    return isinstance(value, str) and re.fullmatch(r'[A-Za-z0-9_-]{1,256}', value) is not None


def rotate(api, claim, *, session_factory=WebSession, login=login_web, journal_factory=Journal):
    task = claim['task_id']
    action = claim.get('action', 'rotate')
    claim = dict(claim)
    candidate = claim.get('candidate_secret', '')
    session_id = claim.get('candidate_enrollment_session_id', '')
    session = None
    stage = 'login_failed'
    try:
        journal = journal_factory()  # Fail BEFORE any upstream mutation if durable storage is unavailable.
        if action == 'rotate':
            journal.save(claim)
            session = session_factory(api.config, claim, claim['totp_secret'])
            api.totp_phase(task, 'enrolling')
            stage = 'enrollment_failed'
            data = session.request('POST', '/backend-api/accounts/mfa/enroll', {'factor_type': 'totp'})
            candidate = data.get('secret', '')
            session_id = data.get('session_id', '')
            # Preserve the raw returned candidate before further validation or activation.
            factor_id = data.get('factor_id', '')
            journal.save(claim, candidate, session_id, factor_id)
            if factor_id and not valid_factor_id(factor_id):
                raise RotationError('enrollment_failed')
            claim['candidate_factor_id'] = factor_id
            if not isinstance(candidate, str) or not isinstance(session_id, str) or not candidate or not session_id:
                raise RotationError('enrollment_failed')
            normalized = lambda value: ''.join(value.upper().split()).rstrip('=')
            if normalized(candidate) == normalized(claim['totp_secret']):
                raise RotationError('enrollment_failed')
            code = totp(candidate)
            api.totp_phase(task, 'prepared', candidate, factor_id, session_id)  # Database commit ACK is required.
            journal.acknowledge(task)
            api.totp_phase(task, 'activating')
            stage = 'activation_unconfirmed'
            activation = session.request('POST', '/backend-api/accounts/mfa/user/activate_enrollment',
                                         {'factor_type': 'totp', 'session_id': session_id, 'code': code})
            if activation.get('success') is not True:
                raise RotationError('activation_unconfirmed')
            api.totp_phase(task, 'verifying')
            # Activation may consume this time window's OTP. Verify once in a
            # fresh window instead of treating replay rejection as a bad binding.
            time.sleep(31 - time.time() % 30)
        elif action in ('verify_new', 'verify_old'):
            api.totp_phase(task, 'verify_recovery')
        else:
            raise RotationError('worker_failed')
        stage = 'verification_failed'
        secret, verified_factor = verify_pair(api.config, claim, candidate, action, login)
        if action != 'verify_old':
            journal.save(claim, candidate, session_id, verified_factor)
        api.totp_phase(task, 'bound', secret, verified_factor)
        if action != 'verify_old':
            journal.acknowledge(task)
        api.totp_finish(task, True)
    except Exception as exc:
        if stage == 'verification_failed' and isinstance(exc, RotationError) and exc.code in (
                'factor_binding_unconfirmed', 'new_accepted_old_valid', 'old_accepted_new_valid'):
            stage = exc.code
        # Never expose network bodies, login traces, candidates, passwords, or exceptions.
        try:
            api.totp_finish(task, False, stage)
        except Exception:
            pass  # The durable lease becomes uncertain, never auto-replayed.
    finally:
        if session is not None:
            try:
                session.close()
            except Exception:
                pass
