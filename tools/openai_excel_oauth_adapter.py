"""Excel PKCE profile for the pinned toSub2 password/TOTP runtime.

Only the temporary protocol copy is changed. Unexpected dependency layouts fail
closed rather than silently falling back to Codex or modifying a shared worker.
"""
import base64
import json
import shutil
import time
from pathlib import Path

CLIENT_ID = "app_fnr0pYvVwwFDocDumLG3H2Bp"
REDIRECT_URI = "https://bps.openai.com/basispoints/extension/360590d7-f8f9-4d88-bf75-0edfe0a4b9f3/auth/callback"


def replace_exact(source, old, new, count=1):
    if source.count(old) != count:
        raise ValueError("unsupported_toSub2_runtime_layout")
    return source.replace(old, new)


def excel_source(source):
    source = replace_exact(source, 'import crypto from "node:crypto";',
                           'import crypto from "node:crypto";\nimport {loginExcelPassword} from "./openai_excel_password_flow.mjs";')
    source = replace_exact(source, 'async function loginChatgptWeb(client, { chatgptBase, authBase, email, rl, password, totpSecret }) {', '''async function loginChatgptWeb(client, { chatgptBase, authBase, email, rl, password, totpSecret }) {
  return loginExcelPassword(client, {authBase,email,rl,password,totpSecret,clientId:DEFAULT_CODEX_CLIENT_ID,redirectUri:DEFAULT_CODEX_REDIRECT_URI},
    {isPasswordLoginPage,isAuthLoginPage,createSentinelHeaders,authJsonStep,getContinueUrl,verifyPassword,
     isMfaChallengePayload,completeTotpMfaIfNeeded,selectChatgptLoginWorkspaceIfNeeded,continueFlow});''')
    source = replace_exact(source, 'async function runCodexOauth(client, options) {', '''async function runCodexOauth(client, options) {
  if (client.excelOAuth) return client.excelOAuth;
  throw new Error("EXCEL_PKCE_SESSION_MISSING");''')
    source = replace_exact(source, 'const DEFAULT_CODEX_CLIENT_ID = "app_EMoamEEZ73f0CkXaXp7hrann";',
                           f'const DEFAULT_CODEX_CLIENT_ID = "{CLIENT_ID}";')
    source = replace_exact(source, 'const DEFAULT_CODEX_REDIRECT_URI = "http://localhost:1455/auth/callback";',
                           f'const DEFAULT_CODEX_REDIRECT_URI = "{REDIRECT_URI}";')
    source = replace_exact(source, 'const state = base64Url(crypto.randomBytes(24));',
                           'const state = "bps." + base64Url(crypto.randomBytes(24)) + ".PC";')
    source = replace_exact(source, '`${options.authBase}/oauth/authorize?`', '`${options.authBase}/api/accounts/authorize?`')
    source = replace_exact(source, '      codex_cli_simplified_flow: "true",\n      id_token_add_organizations: "true",',
                           '      audience: "https://api.openai.com/v1",\n      platform: "PC",')
    source = replace_exact(source, '      scope: "openid profile email offline_access",',
                           '      scope: "openid offline_access email profile organization.read",')
    source = replace_exact(source, '    maxProxySessionAttempts: process.env.CHATGPT_PROXY_MAX_ATTEMPTS || 10,',
                           '    maxProxySessionAttempts: 1,\n    sameProxyRiskRetries: 0,\n    cloudflareSolver: false,')
    # Excel starts at auth.openai.com, without a ChatGPT Web session. The
    # initial probe and both saved-session restart paths must check that same
    # service: a ChatGPT homepage 403 does not establish an OAuth failure.
    source = replace_exact(source,
                           'await transport.prepareProxy(proxyTemplate, `${chatgptBase}/`);',
                           'await transport.prepareProxy(proxyTemplate, `${authBase}/`);', count=3)
    old_callback = '(parsed.hostname === "localhost" || parsed.hostname === "127.0.0.1") &&\n      parsed.pathname === "/auth/callback"'
    source = replace_exact(source, old_callback,
                           'parsed.origin === new URL(DEFAULT_CODEX_REDIRECT_URI).origin &&\n      parsed.pathname === new URL(DEFAULT_CODEX_REDIRECT_URI).pathname && !parsed.username && !parsed.password')
    source = replace_exact(source, '      console.log(codex.callbackUrl);',
                           '      console.log("[excel] OAuth callback received; query withheld.");')
    source = replace_exact(source, '      const sub2apiExport = await buildSub2apiOauthExport({', '''      const callback = new URL(codex.callbackUrl);
      if (!isLocalCallback(codex.callbackUrl) || !codex.state || callback.searchParams.get("state") !== codex.state || !callback.searchParams.get("code")) {
        throw new Error("EXCEL_CALLBACK_STATE_MISMATCH");
      }
      const sub2apiExport = await buildSub2apiOauthExport({''')
    # The pinned dependency has exchange + refresh, each with transport/native branches.
    source = replace_exact(source, '`${authBase}/oauth/token`', '`${authBase}/oauth/token?unified=true`', count=4)
    old_identity = '''  const chatgptAccountId = claims.sid || "";
  const chatgptUserId = authClaims.user_id || claims.sub || "";'''
    source = replace_exact(source, old_identity, '''  const accessClaims = decodeJwtPayload(tokenSet.access_token);
  const accessAuth = accessClaims["https://api.openai.com/auth"] || {};
  const chatgptAccountId = accessAuth.chatgpt_account_id || authClaims.chatgpt_account_id || "";
  const chatgptUserId = accessAuth.chatgpt_user_id || authClaims.chatgpt_user_id || "";
  if (!chatgptAccountId || !chatgptUserId) throw new Error("EXCEL_TOKEN_IDENTITY_MISSING");''')
    source = replace_exact(source, '      access_token: tokenSet.access_token,\n      chatgpt_account_id: chatgptAccountId,', '''      access_token: tokenSet.access_token,
      client_id: clientId,
      expires_at: new Date(accessClaims.exp * 1000).toISOString(),
      chatgpt_user_id: chatgptUserId,
      chatgpt_account_user_id: accessAuth.chatgpt_account_user_id || authClaims.chatgpt_account_user_id || "",
      chatgpt_account_id: chatgptAccountId,''')
    # Fail on any interactive additional verification; never silently hang waiting for email/phone OTP.
    start = 'async function ask(rl, prompt) {'
    if start in source:
        source = replace_exact(source, start, start+'\n  throw new Error("EXCEL_ADDITIONAL_VERIFICATION_REQUIRED");')
    source = replace_exact(source, '  const organization = workspaces.find((item) => item?.kind === "organization" && item?.id);', '''  const expected = process.env.OPENAI_EXCEL_EXPECTED_WORKSPACE;
  if (expected) {
    const matched = workspaces.find((item) => item?.id === expected);
    if (!matched) throw new Error("EXCEL_EXPECTED_WORKSPACE_NOT_LISTED");
    return matched.id;
  }
  const organization = workspaces.find((item) => item?.kind === "organization" && item?.id);''')
    return source


def prepare_excel_runtime(root: Path, temporary: Path) -> Path:
    staged = temporary / "excel-runtime"
    source = (root / "src/protocol-login.mjs").read_text()
    patched = excel_source(source)
    shutil.copytree(root / "src", staged / "src")
    if (root / "node_modules").is_dir():
        (staged / "node_modules").symlink_to((root / "node_modules").resolve(), target_is_directory=True)
    if (root / "LICENSE").is_file():
        shutil.copy2(root / "LICENSE", staged / "LICENSE")
    script = staged / "src/protocol-login.mjs"
    script.write_text(patched)
    shutil.copy2(Path(__file__).with_name("openai_excel_password_flow.mjs"), script.parent / "openai_excel_password_flow.mjs")
    return script


def jwt_payload(token):
    try:
        part = token.split('.')[1]
        value = json.loads(base64.urlsafe_b64decode(part+'='*(-len(part)%4)))
        return value if isinstance(value, dict) else {}
    except (ValueError, TypeError, IndexError, AttributeError):
        return {}


def validate_excel_credentials(credentials, email, expected_workspace=None):
    # Token trust comes from the TLS-authenticated token exchange, not this claim decoder.
    access = jwt_payload(credentials.get('access_token'))
    identity = jwt_payload(credentials.get('id_token'))
    if credentials.get('client_id') != CLIENT_ID or access.get('client_id', access.get('azp')) != CLIENT_ID:
        raise ValueError('excel_client_mismatch')
    audience = identity.get('aud')
    if audience != CLIENT_ID and not (isinstance(audience, list) and CLIENT_ID in audience):
        raise ValueError('excel_id_token_client_mismatch')
    if identity.get('email', '').lower() != email.lower():
        raise ValueError('excel_email_mismatch')
    if not isinstance(access.get('exp'), (float,int)) or access['exp'] <= time.time():
        raise ValueError('excel_token_expired_or_missing_expiry')
    auth = access.get('https://api.openai.com/auth') or {}
    workspace = auth.get('chatgpt_account_id')
    if not workspace or workspace != credentials.get('chatgpt_account_id'):
        raise ValueError('excel_workspace_mismatch')
    if expected_workspace and expected_workspace != workspace:
        raise ValueError('excel_wrong_workspace_selected')
    if not credentials.get('refresh_token'):
        raise ValueError('excel_refresh_missing')
