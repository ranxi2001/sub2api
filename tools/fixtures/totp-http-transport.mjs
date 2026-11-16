// Strict offline HTTP boundary for the actual CLI. Never delegates to fetch.
import fs from 'node:fs';
import assert from 'node:assert/strict';
const fixture=JSON.parse(fs.readFileSync(new URL('./totp-fixture.json',import.meta.url)));
Date.now=()=>59000;
export const shouldUseTlsTransport=()=>true;
export const browserIdentityForTlsProfile=()=>({userAgent:'offline',acceptLanguage:'en-US'});
export class TlsFingerprintTransport {
  enabled=true;
  async prepareProxy() {} async configure() {} async close() {}
  async generateSentinelTokens(){return {token:'synthetic'};}
  async fetch(url,options) {
    const target=new URL(url);
    assert.ok(['https://chatgpt.com','https://auth.openai.com'].includes(target.origin));
    const route=`${options.method} ${target.pathname}`;
    const body=options.body && JSON.parse(options.headers.get('content-type')?.includes('application/json')?options.body:'{}');
    fs.appendFileSync(fixture.trace,JSON.stringify({route,id:body?.id})+'\n');
    if(route==='GET /'||route==='GET /log-in/password'||route==='GET /auth-complete') return new Response('<html>offline</html>');
    if(route==='GET /api/auth/providers') return Response.json({});
    if(route==='GET /api/auth/csrf') return Response.json({csrfToken:'synthetic'}, {headers:{'set-cookie':'__Host-next-auth.csrf-token=synthetic; Path=/; Secure'}});
    if(route==='POST /api/auth/signin/openai') return Response.json({url:'https://auth.openai.com/log-in/password'});
    if(route==='POST /api/accounts/password/verify') return Response.json({page:{type:'mfa_challenge'},'oai-client-auth-session':{mfa_factors:fixture.factors.map(id=>({id,factor_type:'totp'}))}});
    if(route==='POST /api/accounts/mfa/issue_challenge') return Response.json({});
    if(route==='POST /api/accounts/mfa/verify') {
      if(fixture.failure) return Response.json(fixture.error || {error:'invalid_otp'},{status:fixture.failure});
      if(body.code!==fixture.codes[body.id]) return Response.json({error:'invalid_otp'},{status:400});
      return Response.json({continue_url:'https://chatgpt.com/auth-complete'});
    }
    throw Error('UNEXPECTED_OFFLINE_HTTP_REQUEST');
  }
}
