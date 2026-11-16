// Execute the entire pinned protocol module with an offline HTTP client.
// Only CLI startup is disabled; production MFA, TOTP and HTTP-step functions run.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {pathToFileURL} from 'node:url';

const sourcePath = process.env.TOTP_TEST_PROTOCOL;
assert.ok(sourcePath, 'TOTP_TEST_PROTOCOL must name the pinned or patched real module');
const root = await fs.mkdtemp(path.join(os.tmpdir(), 'totp-protocol-'));
let source = await fs.readFile(sourcePath, 'utf8');
assert.equal(source.split('run().catch((error) => {').length, 2);
source = source.replace('run().catch((error) => {', 'Promise.resolve().catch((error) => {');
await fs.writeFile(path.join(root, 'protocol.mjs'), source + '\nexport {ProtocolClient, completeTotpMfaIfNeeded, generateTotp, loginChatgptWeb};\n');
await fs.writeFile(path.join(root, 'tls-transport.mjs'), `export const browserIdentityForTlsProfile=()=>({}); export const shouldUseTlsTransport=()=>false; export class TlsFingerprintTransport { constructor(){throw Error('network forbidden')} }`);
process.env.NODE_ENV = 'test';
process.env.TOSUB2_TEST_SENTINEL_TOKEN = 'synthetic';
const protocol = await import(pathToFileURL(path.join(root, 'protocol.mjs')));
process.on('exit', () => {});
const S1='JBSWY3DPEHPK3PXP', S2='GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ';
function fixture(ids, secret, bound='', failure='') {
  const calls=[];
  const reply=(status,payload)=>({res:{status},text:typeof payload==='string'?payload:JSON.stringify(payload)});
  const client={getJson:protocol.ProtocolClient.prototype.getJson,async request(method,url,{json}) {
    const route=new URL(url).pathname;
    calls.push([route,json.id]);
    if(route.endsWith('/issue_challenge')) return reply(200,{});
    assert.ok(route.endsWith('/mfa/verify'));
    if(failure==='ambiguous-success') return reply(200,{page:{type:'mfa_challenge'}});
    if(typeof failure==='object') return reply(failure.status,failure.payload);
    if(failure==='connection reset') throw Error(failure);
    if(failure) return reply(Number(failure.split(' ')[1]),{error:{code:'invalid_otp'}});
    if(json.code !== protocol.generateTotp(json.id==='F1'?S1:S2)) return reply(400,{error:{code:'invalid_otp'}});
    return reply(200,{continue_url:'https://auth.openai.com/done'});
  }};
  const options={authBase:'https://auth.openai.com',rl:{question(){throw Error('unexpected manual prompt')}},deviceId:'synthetic',
    payload:{page:{type:'mfa_challenge'},'oai-client-auth-session':{mfa_factors:ids.map(id=>({id,factor_type:'totp'}))}},
    totpSecret:secret,referer:'https://auth.openai.com/log-in/password'};
  process.env.CHATGPT_TOTP_FACTOR_ID=bound;
  return {client,options,calls};
}
for(const ids of [['F1','F2'],['F2','F1']]) for(const [secret,id] of [[S1,'F1'],[S2,'F2']]) {
  test(`actual MFA matches ${id} with order ${ids.join(',')}`,async()=>{
    const f=fixture(ids,secret);
    await protocol.completeTotpMfaIfNeeded(f.client,f.options);
    assert.equal(f.client.totpFactorId,id);
    assert.ok(f.calls.length<=4);
  });
}
test('bound login challenges only the bound factor',async()=>{
  const f=fixture(['F1','F2'],S2,'F2');
  await protocol.completeTotpMfaIfNeeded(f.client,f.options);
  assert.deepEqual(f.calls.map(x=>x[1]),['F2','F2']);
});
test('missing bound factor aborts without trying others',async()=>{
  const f=fixture(['F1'],S1,'absent');
  await assert.rejects(protocol.completeTotpMfaIfNeeded(f.client,f.options),/TOTP_FACTOR_NOT_FOUND/);
  assert.equal(f.calls.length,0);
});
test('more than two unbound factors requires explicit binding before challenge',async()=>{
  const f=fixture(['F1','F2','F3'],S1);
  await assert.rejects(protocol.completeTotpMfaIfNeeded(f.client,f.options),/TOTP_FACTOR_BINDING_REQUIRED/);
  assert.equal(f.calls.length,0);
});
test('duplicate factor IDs do not consume extra challenges',async()=>{
  const f=fixture(['F1','F1','F2'],S2);
  await protocol.completeTotpMfaIfNeeded(f.client,f.options);
  assert.deepEqual(f.calls.map(x=>x[1]),['F1','F1','F2','F2']);
});
for(const error of ['HTTP 429 invalid_otp','HTTP 503 invalid_otp','connection reset']) test(`uncertain ${error} never falls back`,async()=>{
  const f=fixture(['F1','F2'],S2,'',error);
  await assert.rejects(protocol.completeTotpMfaIfNeeded(f.client,f.options));
  assert.equal(f.calls.length,2);
});
test('all explicitly rejected stops after two attempts without prompting',async()=>{
  const f=fixture(['F1','F2'],S2,'','HTTP 400 invalid_otp');
  await assert.rejects(protocol.completeTotpMfaIfNeeded(f.client,f.options),/TOTP_REJECTED/);
  assert.equal(f.calls.length,4);
});
test('HTTP 200 with another MFA challenge is not binding evidence',async()=>{
 const f=fixture(['F1','F2'],S2,'','ambiguous-success');
 await assert.rejects(protocol.completeTotpMfaIfNeeded(f.client,f.options),/TOTP_VERIFICATION_UNCONFIRMED/);
 assert.equal(f.client.totpFactorId,undefined);
 assert.equal(f.calls.length,2);
});
for (const [name,payload] of [
 ['explicit session code overrides OTP-like message',{error:{code:'session_expired',message:'incorrect code'}}],
 ['explicit auth code overrides OTP-like message',{error:{code:'authorization_denied',message:'invalid totp'}}],
 ['message alone is not a structured rejection',{error:{message:'incorrect code'}}],
 ['duplicate code is ambiguous','{"error":{"code":"session_expired","code":"invalid_otp","message":"incorrect code"}}'],
]) test(`raw HTTP boundary stops: ${name}`,async()=>{
 const f=fixture(['F1','F2'],S2,'',{status:400,payload});
 await assert.rejects(protocol.completeTotpMfaIfNeeded(f.client,f.options));
 assert.deepEqual(f.calls.map(x=>x[1]),['F1','F1']);
 assert.equal(f.client.totpFactorId,undefined);
});
test.after(async()=>{delete process.env.CHATGPT_TOTP_FACTOR_ID;await fs.rm(root,{recursive:true,force:true});});
