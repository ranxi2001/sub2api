// Private, bounded diagnostic snapshots. Never serialize exception text or URLs.
import fs from 'node:fs';

const stages = new Set(['starting', 'web_login', 'password_accepted', 'mfa_challenge',
  'mfa_accepted', 'web_redirect', 'oauth_authorize', 'session_select',
  'workspace_select', 'callback_validation', 'token_exchange', 'oauth_output', 'completed']);
const reasons = new Set(['none', 'protocol_error', 'password_rejected', 'totp_rejected',
  'interaction_required', 'security_check', 'session_unavailable', 'workspace_mismatch',
  'callback_missing', 'callback_invalid', 'callback_state_mismatch', 'pkce_invalid',
  'token_exchange_failed', 'credentials_incomplete', 'transport_error', 'runtime_error']);

// Codes come only from the actual error envelope, never nested user input.
const operationCodes = {
  phone_verification_required: 'phone_verification_required', phone_number_required: 'phone_verification_required',
  account_deactivated: 'account_deactivated', account_disabled: 'account_deactivated', account_banned: 'account_deactivated',
  account_deleted: 'account_deleted', account_retired: 'account_retired',
  auth_failed: 'requires_relogin', invalid_token: 'requires_relogin', token_invalid: 'requires_relogin',
  requires_relogin: 'requires_relogin', invalid_grant: 'requires_relogin', revoked: 'requires_relogin',
  token_invalidated: 'requires_relogin', token_revoked: 'requires_relogin', refresh_token_reused: 'requires_relogin',
  refresh_token_invalidated: 'requires_relogin', invalid_refresh_token: 'requires_relogin',
  token_expired: 'requires_relogin', app_session_terminated: 'requires_relogin', session_unavailable: 'requires_relogin',
  password_rejected: 'password_rejected', invalid_password: 'password_rejected', incorrect_password: 'password_rejected',
  totp_rejected: 'mfa_required', mfa_required: 'mfa_required', otp_required: 'mfa_required',
  email_verification_required: 'mfa_required', interaction_required: 'mfa_required',
  security_check: 'security_check', security_check_required: 'security_check', challenge_required: 'security_check',
  session_blocked_by_cyber_policy: 'security_check', cyber_policy: 'security_check',
  access_denied: 'authorization_denied', authorization_denied: 'authorization_denied', permission_denied: 'authorization_denied',
  rate_limit_exceeded: 'rate_limited', rate_limit_error: 'rate_limited', rate_limited: 'rate_limited', too_many_requests: 'rate_limited',
  timeout: 'timeout', request_timeout: 'timeout', worker_timeout: 'timeout', idle_timeout: 'timeout',
  transport_error: 'transport_error', connection_error: 'transport_error', proxy_unavailable: 'transport_error',
  server_error: 'service_error', service_unavailable: 'service_error', service_error: 'service_error',
};
const operationTexts = {
  'You must provide a phone number to continue.': 'phone_verification_required',
  'Phone verification required.': 'phone_verification_required',
  'Your account has been deactivated.': 'account_deactivated',
  'Your account has been deleted.': 'account_deleted',
  'Your account has been deleted or deactivated.': 'account_retired',
  'This account has been deleted or deactivated.': 'account_retired',
};
for (const code of Object.values(operationCodes)) reasons.add(code);
const safeCode = value => typeof value === 'string' && Object.hasOwn(operationCodes, value) ? operationCodes[value] : 'unknown';

export function credentialOperationReason(payload) {
  const error = payload?.error;
  if (typeof error === 'string') return safeCode(error);
  if (!error || typeof error !== 'object' || Array.isArray(error)) return 'unknown';
  if (Object.hasOwn(error, 'code')) return safeCode(error.code);
  const type = safeCode(error.type);
  if (type !== 'unknown') return type;
  return typeof error.message === 'string' && Object.hasOwn(operationTexts, error.message)
    ? operationTexts[error.message] : 'unknown';
}

// JSON.parse accepts duplicate fields. Reject them before using any code as
// evidence, including escaped duplicate names; also bound parsing work.
export function parseOperationJSON(raw) {
  if (typeof raw !== 'string' || Buffer.byteLength(raw) > 32768) return null;
  try {
    const value = JSON.parse(raw);
    const tokens = raw.match(/"(?:[^"\\]|\\.)*"|[{}[\]:,]|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|null/g) || [];
    const stack = [];
    for (let i = 0; i < tokens.length; i++) {
      const token = tokens[i];
      if (token === '{') stack.push(new Set());
      else if (token === '[') stack.push(null);
      else if (token === '}' || token === ']') stack.pop();
      else if (token.startsWith('"') && tokens[i + 1] === ':') {
        const keys = stack.at(-1), key = JSON.parse(token);
        if (!keys || keys.has(key)) return null;
        keys.add(key);
      }
      if (stack.length > 32) return null;
    }
    return value;
  } catch { return null; }
}

// Preserve only an explicit OTP rejection at the MFA verification boundary.
// Generic diagnostic mapping must not erase the evidence needed for bounded matching.
export function isTotpRejectionResponse(result) {
  if (![400,401].includes(Number(result?.res?.status))) return false;
  const payload = parseOperationJSON(result?.text);
  const error = payload?.error;
  const code = typeof error === 'string' ? error : error?.code;
  if (['invalid_otp','incorrect_otp','wrong_otp','invalid_totp','incorrect_totp','wrong_totp','invalid_code','incorrect_code','wrong_code'].includes(code)) return true;
  return false; // Descriptive text alone is not structured OTP rejection evidence.
}

export function observeProtocolResponse(result, fallback = 'protocol_error') {
  const payload = parseOperationJSON(result?.text);
  const status = Number(result?.res?.status);
  const code = credentialOperationReason(payload);
  if (code !== 'unknown') throw new Error(code);
  if (!payload?.error && payload?.page?.type === 'add_phone') throw new Error('phone_verification_required');
  if (status >= 400 || payload?.error) {
    throw new Error(status === 429 ? 'rate_limited' : status >= 500 ? 'service_error' : fallback);
  }
}

function reasonFor(error, stage) {
  try {
    if (reasons.has(error?.message)) return error.message;
    const text = typeof error?.message === 'string' ? error.message : '';
    if (text.startsWith('TOTP_REJECTED:')) return 'totp_rejected';
    if (text.startsWith('TOTP_FACTOR_') || text === 'TOTP_VERIFICATION_UNCONFIRMED') return 'interaction_required';
    if (text.startsWith('PROXY_RISK_CONTROL: ')) return 'security_check';
    if (/^(?:CODEX_AUTH_LOGIN_REQUIRED|SESSION_SELECTION_INVALID):/.test(text)) return 'session_unavailable';
    if (['ECONNREFUSED', 'ENOTFOUND', 'ETIMEDOUT'].includes(error?.code) || ['fetch failed', 'timed out'].includes(text)) return 'transport_error';
    if (['ERR_MODULE_NOT_FOUND', 'MODULE_NOT_FOUND', 'ENOENT'].includes(error?.code)) return 'runtime_error';
    if (stage === 'token_exchange') return 'token_exchange_failed';
  } catch { /* Even a hostile exception getter cannot enter the channel. */ }
  return 'protocol_error';
}

export function createProtocolDiagnostics(file, taskId) {
  const started = performance.now();
  let stage = 'starting';
  function write(reason) {
    if (typeof file !== 'string' || !Number.isSafeInteger(taskId) || taskId <= 0) return;
    const value = {version: 1, task_id: taskId, stage, reason,
      elapsed_ms: Math.min(3600000, Math.max(0, Math.floor(performance.now() - started)))};
    try {
      fs.writeFileSync(file + '.tmp', JSON.stringify(value), {mode: 0o600});
      fs.renameSync(file + '.tmp', file);
    } catch { /* Optional diagnostics must not change an authentication result. */ }
  }
  write('none');
  return {
    mark(next) {
      if (!stages.has(next)) return;
      stage = next;
      write('none');
    },
    fail(error) { write(reasonFor(error, stage)); },
  };
}

export function validateCodexCallback(codex, redirectUri) {
  if (!codex || typeof codex.callbackUrl !== 'string' || !codex.callbackUrl) {
    throw new Error('callback_missing');
  }
  let callback, expected;
  try { callback = new URL(codex.callbackUrl); expected = new URL(redirectUri); }
  catch { throw new Error('callback_invalid'); }
  if (callback.origin !== expected.origin || callback.pathname !== expected.pathname ||
      callback.username || callback.password || callback.hash) throw new Error('callback_invalid');
  if (callback.searchParams.has('error')) throw new Error('protocol_error');
  if (typeof codex.state !== 'string' || !codex.state ||
      callback.searchParams.getAll('state').length !== 1 ||
      callback.searchParams.get('state') !== codex.state) throw new Error('callback_state_mismatch');
  if (callback.searchParams.getAll('code').length !== 1 ||
      !callback.searchParams.get('code')?.trim()) throw new Error('callback_missing');
  if (typeof codex.codeVerifier !== 'string' || !/^[A-Za-z0-9._~-]{43,128}$/.test(codex.codeVerifier)) {
    throw new Error('pkce_invalid');
  }
}
