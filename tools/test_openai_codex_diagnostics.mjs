import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import {createProtocolDiagnostics, validateCodexCallback, credentialOperationReason, parseOperationJSON, observeProtocolResponse} from './openai_codex_diagnostics.mjs';
import {resolveCodexLoginPage} from './openai_codex_login.mjs';

test('login entry never posts identity or follows a password page on an untrusted origin', async () => {
  const options = {authBase: 'https://auth.openai.com', email: 'test@example.test', deviceId: 'synthetic-device'};
  const helpers = {
    createSentinelHeaders: async () => ({}),
    authJsonStep: async () => { throw new Error('UNEXPECTED_IDENTITY_SUBMISSION'); },
    getContinueUrl: data => data.continue_url,
  };
  const client = {follow: async () => { throw new Error('UNEXPECTED_FOLLOW'); }};
  for (const finalUrl of ['https://untrusted.example/log-in', 'https://untrusted.example/log-in/password',
    'http://auth.openai.com/log-in', 'https://user@auth.openai.com/log-in', 'https://chatgpt.com/log-in']) {
    await assert.rejects(resolveCodexLoginPage(client, {finalUrl}, options, helpers), {message: 'protocol_error'});
  }
  for (const continue_url of ['https://untrusted.example/log-in/password', '//untrusted.example/log-in/password',
    'https://user@auth.openai.com/log-in/password', '/log-in/password#private', '/unknown', '']) {
    await assert.rejects(resolveCodexLoginPage(client, {finalUrl: options.authBase + '/log-in'}, options,
      {...helpers, authJsonStep: async () => ({data: {continue_url}})}), {message: 'protocol_error'});
  }
  await assert.rejects(resolveCodexLoginPage(
    {follow: async () => ({finalUrl: 'https://untrusted.example/log-in/password'})},
    {finalUrl: options.authBase + '/log-in'}, options,
    {...helpers, authJsonStep: async () => ({data: {continue_url: '/log-in/password'}})}), {message: 'protocol_error'});
});

test('email verification remains distinct from TOTP in the private diagnostic', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'codex-email-test-'));
  try {
    const file = path.join(root, 'diagnostic.json');
    const channel = createProtocolDiagnostics(file, 7);
    channel.mark('email_verification');
    channel.fail(new Error('email_otp_required'));
    const value = JSON.parse(fs.readFileSync(file));
    assert.equal(value.stage, 'email_verification');
    assert.equal(value.reason, 'email_otp_required');
    channel.fail(new Error('email_otp_required synthetic-private'));
    assert.equal(JSON.parse(fs.readFileSync(file)).reason, 'protocol_error');
  } finally { fs.rmSync(root, {recursive: true, force: true}); }
});

test('actual error envelopes classify only codes and full allowlisted messages', () => {
  const fixtures = JSON.parse(fs.readFileSync(new URL('./fixtures/credential-operation-errors.json', import.meta.url)));
  for (const {name, payload, code} of fixtures) {
    assert.equal(credentialOperationReason(payload), code, name);
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'operation-test-'));
    try {
      const file = path.join(root, 'diagnostic.json');
      const channel = createProtocolDiagnostics(file, 7);
      channel.mark('web_login');
      try { observeProtocolResponse({res: {status: 400}, text: JSON.stringify(payload)}); }
      catch (error) { channel.fail(error); }
      const diagnostic = JSON.parse(fs.readFileSync(file));
      assert.equal(diagnostic.reason, code === 'unknown' ? 'protocol_error' : code, name);
      assert.ok(!JSON.stringify(diagnostic).includes('synthetic-private'));
    } finally { fs.rmSync(root, {recursive: true, force: true}); }
  }
  for (const raw of ['{"error":{"code":"account_deleted","code":"account_deactivated"}}',
    '{"error":{"co\\u0064e":"account_deleted","code":"account_deactivated"}}',
    '{"error":{"code":"account_deleted"},"error":{"code":"account_retired"}}', 'x'.repeat(32769)]) {
    assert.equal(parseOperationJSON(raw), null);
  }
  assert.throws(() => observeProtocolResponse({res: {status: 200}, text: '{"page":{"type":"add_phone"}}'}), {message: 'phone_verification_required'});
  for (const text of ['Your account has not been deleted.', 'User said: "account_deleted"', 'No security check required']) {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'negative-test-'));
    try {
      const file = path.join(root, 'diagnostic.json');
      const channel = createProtocolDiagnostics(file, 7);
      channel.fail(new Error(text));
      assert.equal(JSON.parse(fs.readFileSync(file)).reason, 'protocol_error');
    } finally { fs.rmSync(root, {recursive: true, force: true}); }
  }
});

test('callback requires the bound state, origin, single code and PKCE verifier', () => {
  const redirect = 'http://localhost:1455/auth/callback';
  const valid = {callbackUrl: redirect + '?code=synthetic-code&state=synthetic-state',
    state: 'synthetic-state', codeVerifier: 'v'.repeat(64)};
  assert.doesNotThrow(() => validateCodexCallback(valid, redirect));
  for (const [change, reason] of [
    [{callbackUrl: ''}, 'callback_missing'],
    [{callbackUrl: redirect + '?state=synthetic-state'}, 'callback_missing'],
    [{state: 'wrong-state'}, 'callback_state_mismatch'],
    [{callbackUrl: valid.callbackUrl + '&state=other'}, 'callback_state_mismatch'],
    [{callbackUrl: valid.callbackUrl + '&code=other'}, 'callback_missing'],
    [{callbackUrl: 'https://other.example/callback?code=secret'}, 'callback_invalid'],
    [{callbackUrl: valid.callbackUrl + '#synthetic-private'}, 'callback_invalid'],
    [{codeVerifier: ''}, 'pkce_invalid'],
  ]) {
    assert.throws(() => validateCodexCallback({...valid, ...change}, redirect), {message: reason});
  }
});

test('private snapshots contain only bounded enums, time and the task binding', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'codex-diagnostic-test-'));
  try {
    const file = path.join(root, 'diagnostic.json');
    const channel = createProtocolDiagnostics(file, 7);
    channel.mark('mfa_accepted');
    channel.mark('synthetic-private'); // Unknown progress cannot inject content.
    channel.fail(new Error('synthetic-private https://example.test/?code=private'));
    let value = JSON.parse(fs.readFileSync(file, 'utf8'));
    assert.deepEqual(Object.keys(value).sort(), ['elapsed_ms', 'reason', 'stage', 'task_id', 'version']);
    assert.equal(value.stage, 'mfa_accepted');
    assert.equal(value.reason, 'protocol_error');
    assert.equal(value.task_id, 7);
    assert.ok(Number.isInteger(value.elapsed_ms) && value.elapsed_ms <= 3600000);
    assert.ok(!fs.readFileSync(file, 'utf8').includes('private'));
    assert.equal(fs.statSync(file).mode & 0o777, 0o600);
    channel.mark('token_exchange');
    channel.fail(new Error('synthetic-provider-body'));
    value = JSON.parse(fs.readFileSync(file, 'utf8'));
    assert.equal(value.reason, 'token_exchange_failed');
    channel.fail({get message() { throw new Error('synthetic-private'); }});
    assert.equal(JSON.parse(fs.readFileSync(file, 'utf8')).reason, 'protocol_error');
  } finally { fs.rmSync(root, {recursive: true, force: true}); }
});

test('only explicit 400/401 OTP rejection permits another factor', async () => {
 const {isTotpRejectionResponse}=await import('./openai_codex_diagnostics.mjs');
 const response=(status,error)=>({res:{status},text:JSON.stringify({error})});
 assert.equal(isTotpRejectionResponse(response(400,{code:'invalid_otp'})),true);
 assert.equal(isTotpRejectionResponse(response(401,{message:'invalid totp'})),false);
 for (const value of [response(429,{code:'invalid_otp'}),response(500,{code:'invalid_otp'}),response(400,{code:'unknown',message:'invalid totp'}),response(400,{message:'nested invalid otp data'})]) {
  assert.equal(isTotpRejectionResponse(value),false);
 }
});
