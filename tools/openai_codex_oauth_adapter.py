"""Instrument only the pinned Codex CLI in a private per-task copy.

The diagnostic channel contains enumerations, elapsed time and task identity;
it is not a transport for the external runner's stdout/stderr or exceptions.
"""
import hashlib
import json
import os
import shutil
import stat
from pathlib import Path

try:
    from .openai_excel_oauth_adapter import replace_exact
except ImportError:
    from openai_excel_oauth_adapter import replace_exact

PINNED_PROTOCOL_BLOB = 'ff2db2507e401c29fa78705e44f5b40a3108a8c6'
BOUND_PROTOCOL_BLOB = '7d5143da87e36ad79c2d050003df99319736b866'
STAGES = frozenset(('starting', 'web_login', 'password_accepted', 'mfa_challenge',
                   'mfa_accepted', 'web_redirect', 'oauth_authorize', 'session_select',
                   'workspace_select', 'callback_validation', 'token_exchange', 'oauth_output', 'completed'))
REASONS = frozenset(('none', 'protocol_error', 'password_rejected', 'totp_rejected',
                    'interaction_required', 'security_check', 'session_unavailable', 'workspace_mismatch',
                    'callback_missing', 'callback_invalid', 'callback_state_mismatch', 'pkce_invalid',
                    'token_exchange_failed', 'credentials_incomplete', 'transport_error', 'runtime_error',
                    'process_failed', 'timeout', 'diagnostic_invalid', 'output_invalid',
                    'phone_verification_required', 'account_deactivated', 'account_deleted', 'account_retired',
                    'requires_relogin', 'mfa_required', 'authorization_denied', 'rate_limited', 'service_error'))
MAX_DIAGNOSTIC_BYTES = 1024


def codex_source(source):
    raw = source.encode('utf-8')
    if hashlib.sha1(b'blob ' + str(len(raw)).encode() + b'\0' + raw).hexdigest() not in (PINNED_PROTOCOL_BLOB, BOUND_PROTOCOL_BLOB):
        raise ValueError('unsupported_codex_runtime_layout')
    source = replace_exact(source, 'import crypto from "node:crypto";', '''import crypto from "node:crypto";
import {createProtocolDiagnostics, validateCodexCallback, observeProtocolResponse, isTotpRejectionResponse} from "./openai_codex_diagnostics.mjs";
const protocolDiagnostic = createProtocolDiagnostics(process.env.SUB2API_PROTOCOL_DIAGNOSTIC_PATH,
  Number(process.env.SUB2API_PROTOCOL_TASK_ID));
console.log = console.warn = console.error = () => {};''')
    for old, new in (
        ('    assertOk(result, `${method} ${url}`);', '    if (new URL(url).pathname === "/api/accounts/mfa/verify" && isTotpRejectionResponse(result)) throw new Error(`HTTP ${result.res.status} invalid_otp`);\n    observeProtocolResponse(result);\n    assertOk(result, `${method} ${url}`);'),
        ('async function loginChatgptWeb(client, { chatgptBase, authBase, email, rl, password, totpSecret }) {',
         'async function loginChatgptWeb(client, { chatgptBase, authBase, email, rl, password, totpSecret }) {\n  protocolDiagnostic.mark("web_login");'),
        ('      console.log("[ok] Password accepted");', '      protocolDiagnostic.mark("password_accepted");'),
        ('  console.log("[mfa] TOTP 2FA challenge reached.");', '  protocolDiagnostic.mark("mfa_challenge");'),
        ('      console.log("[ok] 2FA verification accepted");', '      protocolDiagnostic.mark("mfa_accepted");'),
        ('  const callback = await continueFlow(client, authenticated);', '  protocolDiagnostic.mark("web_redirect");\n  const callback = await continueFlow(client, authenticated);'),
        ('async function runCodexOauth(client, options) {', 'async function runCodexOauth(client, options) {\n  protocolDiagnostic.mark("oauth_authorize");'),
        ('  const { data: selected } = await authJsonStep(client, options.authBase, "POST", "/api/accounts/session/select", {',
         '  protocolDiagnostic.mark("session_select");\n  const { data: selected } = await authJsonStep(client, options.authBase, "POST", "/api/accounts/session/select", {'),
        ('  const workspaceId = pickWorkspaceId(current);', '  protocolDiagnostic.mark("workspace_select");\n  const workspaceId = pickWorkspaceId(current);'),
        ('    if (codex?.callbackUrl) {', '''    protocolDiagnostic.mark("callback_validation");
    validateCodexCallback(codex, args.codexRedirectUri || DEFAULT_CODEX_REDIRECT_URI);
    if (codex?.callbackUrl) {'''),
        ('async function exchangeOAuthCode({ authBase, clientId, code, codeVerifier, redirectUri, transport, cookie }) {',
         'async function exchangeOAuthCode({ authBase, clientId, code, codeVerifier, redirectUri, transport, cookie }) {\n  protocolDiagnostic.mark("token_exchange");'),
        ('      await writeJsonAtomic(sub2apiOutPath, sub2apiExport.data);',
         '      protocolDiagnostic.mark("oauth_output");\n      await writeJsonAtomic(sub2apiOutPath, sub2apiExport.data);\n      protocolDiagnostic.mark("completed");'),
        ('    const message = data?.error_description || data?.error || JSON.stringify(data).slice(0, 180);', '    observeProtocolResponse({res, text}, "token_exchange_failed");\n    const message = data?.error_description || data?.error || JSON.stringify(data).slice(0, 180);'),
        ('async function ask(rl, prompt) {', 'async function ask(rl, prompt) {\n  throw new Error("interaction_required");'),
        ('      if (!isRejectedPasswordError(error)) throw error;',
         '      if (!isRejectedPasswordError(error)) throw error;\n      throw new Error("password_rejected");'),
        ('      if (!isRejectedTotpError(error)) throw error;',
         '      if (!isRejectedTotpError(error)) throw error;\n      throw new Error("totp_rejected");'),
        ('    maxProxySessionAttempts: process.env.CHATGPT_PROXY_MAX_ATTEMPTS || 10,',
         '    maxProxySessionAttempts: 1,\n    sameProxyRiskRetries: 0,\n    cloudflareSolver: false,'),
        ('    if (!data[key]) throw new Error(`Token response missing ${key}.`);',
         '    if (typeof data[key] !== "string" || !data[key].trim()) throw new Error("credentials_incomplete");'),
    ):
        if old == '      if (!isRejectedTotpError(error)) throw error;' and 'TOTP_FACTOR_BINDING_REQUIRED' in source:
            continue  # The tracked bounded MFA flow already terminates explicit rejection.
        source = replace_exact(source, old, new)
    # Both saved-session and freshly authenticated-session failures must end the
    # queued attempt. Do not turn a rejected OAuth result into another login.
    source = replace_exact(source, '''      } catch (error) {
        if (isSecurityCheckRequiredError(error)) throw error;
        if (!isExpiredCheckpointError(error)) throw error;
        console.log("[auth-expired] Saved login state expired; restarting email login.");
        await removeProtocolCheckpoint(checkpointPath);
        checkpoint = null;
          client = null;
          codex = null;
          await transport.prepareProxy(proxyTemplate, `${chatgptBase}/`);
          }''', '''      } catch (error) {
        throw error;
      }''')
    source = replace_exact(source, '''        } catch (error) {
          if (isSecurityCheckRequiredError(error)) throw error;
          if (!isExpiredCheckpointError(error)) throw error;
          await removeProtocolCheckpoint(checkpointPath);
          if (freshLoginRestartCount >= 1) throw error;
          freshLoginRestartCount += 1;
          console.log("[auth-expired] Newly verified login state was rejected; restarting email login once.");
          client = null;
          web = null;
          codex = null;
          await transport.prepareProxy(proxyTemplate, `${chatgptBase}/`);
          continue;
        }''', '''        } catch (error) {
          throw error;
        }''')
    source = replace_exact(source, '    let freshLoginRestartCount = 0;\n', '')
    source = replace_exact(source, '  const organization = workspaces.find((item) => item?.kind === "organization" && item?.id);', '''  const expected = process.env.OPENAI_CODEX_EXPECTED_WORKSPACE;
  if (expected) {
    const matched = workspaces.find(item => item?.id === expected);
    if (!matched) throw new Error("workspace_mismatch");
    return matched.id;
  }
  const organization = workspaces.find((item) => item?.kind === "organization" && item?.id);''')
    source = replace_exact(source, '''run().catch((error) => {
  if (isProxyRiskControlError(error)) {
    console.error(`[proxy-risk-retry] ${String(error.message || "Proxy security check").slice(0, 320)}`);
  } else {
    console.error(`[error] ${error.message}`);
  }
  process.exitCode = 1;
});''', '''run().catch((error) => {
  protocolDiagnostic.fail(error);
  process.exitCode = 1;
});''')
    return source


def prepare_codex_runtime(root: Path, temporary: Path) -> Path:
    try:
        patched = codex_source((root / 'src/protocol-login.mjs').read_text())
    except (OSError, UnicodeError):
        raise ValueError('unsupported_codex_runtime_layout') from None
    staged = temporary / 'codex-runtime'
    shutil.copytree(root / 'src', staged / 'src')
    if (root / 'node_modules').is_dir():
        (staged / 'node_modules').symlink_to((root / 'node_modules').resolve(), target_is_directory=True)
    if (root / 'LICENSE').is_file():
        shutil.copy2(root / 'LICENSE', staged / 'LICENSE')
    script = staged / 'src/protocol-login.mjs'
    script.write_text(patched)
    shutil.copy2(Path(__file__).with_name('openai_codex_diagnostics.mjs'), script.parent)
    return script


def read_protocol_diagnostic(path: Path, task_id: int):
    try:
        if path.is_symlink():
            raise ValueError('invalid_protocol_diagnostic')
        descriptor = os.open(path, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0) | getattr(os, 'O_NONBLOCK', 0))
        with os.fdopen(descriptor, 'rb') as stream:
            if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
                raise ValueError('invalid_protocol_diagnostic')
            raw = stream.read(MAX_DIAGNOSTIC_BYTES + 1)
    except FileNotFoundError:
        return None  # Older runners may omit this optional side channel.
    except (OSError, ValueError):
        raise ValueError('invalid_protocol_diagnostic') from None
    try:
        if len(raw) > MAX_DIAGNOSTIC_BYTES:
            raise ValueError
        def unique_object(pairs):
            result = {}
            for key, value in pairs:
                if key in result:
                    raise ValueError
                result[key] = value
            return result
        data = json.loads(raw, object_pairs_hook=unique_object)
        if not isinstance(data, dict) or set(data) != {'version', 'task_id', 'stage', 'reason', 'elapsed_ms'}:
            raise ValueError
        if type(data['version']) is not int or data['version'] != 1:
            raise ValueError
        if type(data['task_id']) is not int or data['task_id'] != task_id:
            raise ValueError
        if not isinstance(data['stage'], str) or data['stage'] not in STAGES:
            raise ValueError
        if not isinstance(data['reason'], str) or data['reason'] not in REASONS:
            raise ValueError
        if type(data['elapsed_ms']) is not int or not 0 <= data['elapsed_ms'] <= 3600000:
            raise ValueError
        return data
    except (ValueError, TypeError, UnicodeError):
        raise ValueError('invalid_protocol_diagnostic') from None


def protocol_failure(diagnostic, exit_code, elapsed_ms, reason=None):
    stage = diagnostic['stage'] if diagnostic else 'starting'
    code = reason or (diagnostic['reason'] if diagnostic else 'process_failed')
    if code == 'none':
        code = 'process_failed'
    if stage not in STAGES or code not in REASONS:
        stage, code = 'starting', 'diagnostic_invalid'
    return (f'local_protocol_failed stage={stage} reason={code} '
            f'elapsed_ms={min(3600000, max(0, int(elapsed_ms)))} exit={max(-255, min(255, int(exit_code)))}')
