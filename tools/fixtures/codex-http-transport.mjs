// The pinned CLI's complete HTTP boundary. Every unlisted request fails offline.
import assert from 'node:assert/strict';
import fs from 'node:fs';
export const shouldUseTlsTransport = () => true;
export const browserIdentityForTlsProfile = () => ({userAgent: 'offline-test', acceptLanguage: 'en-US'});
let state;
const jwt = claims => 'e30.' + Buffer.from(JSON.stringify(claims)).toString('base64url') + '.synthetic';
export class TlsFingerprintTransport {
  enabled = true;
  async prepareProxy() {}
  async configure() {}
  async close() {}
  async generateSentinelTokens() { return {token: 'synthetic-sentinel'}; }
  async fetch(url, options) {
    const target = new URL(url);
    assert.ok(['https://chatgpt.com', 'https://auth.openai.com'].includes(target.origin));
    const route = `${options.method} ${target.pathname}`;
    fs.appendFileSync(process.env.CODEX_TEST_ROUTES, route + '\n');
    const mode = process.env.CODEX_TEST_MODE;
    const html = text => new Response(text || '<html>Offline</html>', {headers: {'content-type': 'text/html'}});
    const callback = () => 'http://localhost:1455/auth/callback?' + new URLSearchParams({
      ...(mode === 'missing-callback' ? {} : {code: 'synthetic-code'}),
      state: mode === 'wrong-state' ? 'wrong-state' : state,
    });
    if (route === 'GET /') return html();
    if (route === 'GET /api/auth/providers') return Response.json({});
    if (route === 'GET /api/auth/csrf') return Response.json({csrfToken: 'synthetic-csrf'}, {
      headers: {'set-cookie': '__Host-next-auth.csrf-token=synthetic-csrf; Path=/; Secure'},
    });
    if (route === 'POST /api/auth/signin/openai') {
      if (mode.startsWith('login-entry-')) {
        return Response.json({url: target.searchParams.has('login_hint')
          ? 'https://auth.openai.com/email-verification' : 'https://auth.openai.com/log-in'});
      }
      return Response.json({url: 'https://auth.openai.com/log-in/password'});
    }
    if (route === 'GET /log-in/password' || route === 'GET /log-in') return html();
    if (route === 'GET /email-verification') return html();
    if (route === 'POST /api/accounts/authorize/continue') {
      assert.equal(target.origin, 'https://auth.openai.com');
      assert.deepEqual(JSON.parse(options.body), {username: {kind: 'email', value: 'test@example.test'}});
      assert.ok(options.headers.get('openai-sentinel-token'));
      if (mode === 'login-entry-rejected') return Response.json({error: {code: 'account_deactivated'}}, {status: 403});
      const destinations = {
        'login-entry-email': 'https://auth.openai.com/email-verification',
        'login-entry-untrusted': 'https://untrusted.example/log-in/password',
        'login-entry-missing': '',
      };
      return Response.json({continue_url: destinations[mode] ?? 'https://auth.openai.com/log-in/password'});
    }
    if (route === 'POST /api/accounts/password/verify') {
      assert.equal(JSON.parse(options.body).password, 'synthetic-password');
      if (mode === 'operation-error') return Response.json(JSON.parse(process.env.CODEX_TEST_ERROR), {status: 400});
      return Response.json({page: {type: 'mfa_challenge'}, 'oai-client-auth-session': {
        mfa_factors: [...(mode === 'multi-factor' ? [{factor_type:'totp',id:'old-factor'}] : []), {factor_type: 'totp', id: 'synthetic-factor'}],
      }});
    }
    if (route === 'POST /api/accounts/mfa/issue_challenge') return Response.json({});
    if (route === 'POST /api/accounts/mfa/verify') {
      if (JSON.parse(options.body).id === 'old-factor') return Response.json({error:{code:'invalid_otp'}},{status:400});
      assert.match(JSON.parse(options.body).code, /^\d{6}$/);
      return Response.json({continue_url: 'https://chatgpt.com/auth-complete'});
    }
    if (route === 'GET /auth-complete') return html();
    if (route === 'GET /oauth/authorize') {
      state = target.searchParams.get('state');
      assert.ok(state);
      assert.equal(target.searchParams.get('code_challenge_method'), 'S256');
      assert.ok(target.searchParams.get('code_challenge'));
      if (mode === 'session-rejected') return new Response('', {status: 302, headers: {location: '/log-in'}});
      if (mode.startsWith('workspace-')) return html('<input name="session_id" value="us_synthetic_session">');
      return new Response('', {status: 302, headers: {location: callback()}});
    }
    if (route === 'POST /api/accounts/session/select') return Response.json({
      'oai-client-auth-session': {workspaces: [{id: mode === 'workspace-success' ? 'expected-workspace' : 'other-workspace', kind: 'personal'}]},
    });
    if (route === 'POST /api/accounts/workspace/select') {
      assert.equal(JSON.parse(options.body).workspace_id, 'expected-workspace');
      return Response.json({continue_url: callback()});
    }
    if (route === 'POST /oauth/token') {
      const form = new URLSearchParams(options.body);
      assert.equal(form.get('code'), 'synthetic-code');
      assert.match(form.get('code_verifier'), /^[A-Za-z0-9._~-]{43,128}$/);
      if (mode === 'exchange-failed') return Response.json({error: 'synthetic-private'}, {status: 400});
      return Response.json({access_token: 'synthetic-access',
        ...(mode === 'incomplete' ? {} : {refresh_token: 'synthetic-refresh'}),
        id_token: jwt({email: 'test@example.test', sid: 'synthetic-account', exp: 4000000000,
          'https://api.openai.com/auth': {user_id: 'synthetic-user'}})});
    }
    throw new Error('UNEXPECTED_OFFLINE_HTTP_REQUEST');
  }
}
