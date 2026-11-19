export async function resolveCodexLoginPage(client, page, options, helpers) {
  const {authBase, email, deviceId} = options;
  const current = new URL(page.finalUrl);
  if (authBase !== 'https://auth.openai.com') throw new Error('protocol_error');
  if (![authBase, 'https://chatgpt.com'].includes(current.origin) || current.username || current.password || current.hash) {
    throw new Error('protocol_error');
  }
  if (!['/log-in', '/log-in-or-create-account'].includes(current.pathname)) return page;
  if (current.origin !== authBase || current.username || current.password) throw new Error('protocol_error');
  const headers = await helpers.createSentinelHeaders(client, {authBase, deviceId, flow: 'authorize_continue'});
  const {data} = await helpers.authJsonStep(client, authBase, 'POST', '/api/accounts/authorize/continue',
    {username: {kind: 'email', value: email}}, {referer: page.finalUrl, headers});
  const next = helpers.getContinueUrl(data);
  if (!next) throw new Error('protocol_error');
  const target = new URL(next, authBase);
  if (target.origin !== authBase || target.username || target.password || target.hash ||
      !['/log-in/password', '/email-verification'].includes(target.pathname)) throw new Error('protocol_error');
  const resolved = await client.follow(target.toString(), {referer: page.finalUrl});
  const final = new URL(resolved.finalUrl);
  if (final.origin !== authBase || final.username || final.password || final.hash ||
      !['/log-in/password', '/email-verification'].includes(final.pathname)) throw new Error('protocol_error');
  return resolved;
}
