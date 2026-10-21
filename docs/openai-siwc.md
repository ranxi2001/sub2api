# OpenAI SIWC protocol

SIWC (Sign in with ChatGPT) lets an eligible subscriber authorize ChatGPT plan
usage through `https://api.openai.com/v1/responses`. This is a separate OAuth
grant from Codex and Excel/BPS. A Codex token cannot be converted by editing the
client ID or changing the upstream URL.

## Account editing

Open an OpenAI OAuth parent account and use the **OpenAI request protocol** panel:

1. Choose **Authorize with saved password / 2FA** to reuse Credential Guard's
   encrypted password and TOTP. This task always uses the packaged local worker,
   even when the global Codex login engine is Session Studio. The configured
   login proxy applies to this authorization; inference uses the account proxy.
2. Initial consent, an account selector that requires interaction, or another
   verification challenge may require **Browser authorization**. Follow
   **Open ChatGPT authorization**, or copy the full authorization URL shown in
   the editor into a browser. Keep the editor open, authorize the intended subscriber/workspace, then
   paste the complete `http://127.0.0.1:1455/auth/callback?...` URL from the browser
   address bar. The loopback page need not load. The server validates the URL,
   state, issued client ID, PKCE, signed ID token, nonce and account identity.
3. Once bound, select **SIWC API** and save the account. Selecting **Codex** and
   saving restores the existing Codex/BPS/Prism routing configuration.

Authorization stores a grant but does not silently change the selected protocol.
The UI's bound status means credentials are present; it is not an entitlement or
live availability check. The existing account connection test uses the selected
protocol and requires a completed Responses stream.

Returning authorization retains the host ID and issued OAuth client ID, and
uses the retained ID token as an account hint. Normal requests refresh expiring
access tokens without password/TOTP login. A revoked refresh token or a policy
challenge requires renewed authorization; no different billing path is selected.

SIWC access-token authentication metadata is opaque. It does not expose the
Codex `chatgpt_account_id` / `chatgpt_user_id` fields and is not decoded to prove
identity. The token client verifies the ID-token signature, issuer, issued-client
audience, expiry and nonce. First binding requires its email to match the parent
account's stored email; subsequent authorization also pins the verified subject.
Refresh retains that identity when no new ID token is returned, and rejects a
changed subject or email when one is returned. Access-token expiry comes from
the validated token response's `expires_in`.

The issued client ID pins the workspace selected during official registration.
The locally saved `workspace` is the parent-account snapshot used to invalidate
stale local bindings if that account changes; it is not an extracted or verified
upstream workspace claim. Administrators must choose the intended workspace in
the official first-time consent flow. Same-email workspace equality cannot be
inferred from the opaque metadata or from the ID-token subject.

Worker failures report fixed `stage` and `reason` labels without copying raw
runner output. `browser_interaction_required` after `mfa_accepted` means the
password and TOTP steps completed but further browser interaction is needed;
it does not mean the account is bound. Other reasons distinguish credential
rejection, workspace mismatch, network/security checks and runtime failures.
For a fixed proxy, a preflight HTTP 403 with a security challenge also reports
`security_check` and recommends browser authorization. A normal browser may
reach the login page through the same proxy even when the protocol Worker
cannot; this error alone does not establish that the saved password or TOTP
was rejected. Completing browser authorization stores the grant for normal
token refresh without repeating the password flow.
An old automatic-task error is hidden while a new browser session is displayed;
callback failures remain visible and preserve the entered callback for correction.

## Storage and concurrency

Migration `292_openai_siwc.sql` adds the `siwc` worker task profile and a separate
`openai_siwc_credentials` table. Grants are encrypted with the existing durable
credential encryption key. Database backups must retain that key. Ordinary
account API responses and account exports do not contain this separate grant.
Codex and Excel grants, passwords, TOTP seeds, account status and scheduling
flags are not overwritten by SIWC binding or refresh.

Each session is bound to the target account's credential snapshot and previous
SIWC grant. Sessions are consumed atomically; account/grant changes reject stale
binding writes. Refresh checks the current account identity and SIWC grant, so
a simultaneous primary Codex token refresh does not discard a rotated SIWC token. Task ownership and completion are committed with the grant in one SQL
transaction. A process mutex plus the shared token-cache lease serializes
rotating refresh tokens across API replicas. If refresh persistence fails,
unsaved credentials are not returned to inference. A 401 marks only the rejected
SIWC access token for refresh; it does not revoke the primary Codex grant.

Authorization sessions currently live in the API process memory, matching the
existing OpenAI OAuth service. Route browser callbacks and worker completion to
the API replica that created the session. Restarting that process requires a
fresh authorization. Shared database storage alone does not share pending PKCE
sessions between replicas.

## Request behavior

The Responses, Chat Completions and Anthropic Messages gateways use the same
SIWC token and official Responses endpoint. HTTP requests set `store=false` and
`stream=true`; existing adapters preserve the downstream streaming preference.
System messages become developer messages, configuration-update effort becomes
request-level reasoning, and unsupported request parameters are removed.
Function/custom tools are grouped in namespaces; unsupported hosted tools return
a local error. HTTP requests carry their conversation history explicitly.

This integration uses HTTP/SSE. WebSocket selection is disabled for SIWC.
Compression (`/responses/compact`), native image generation, hosted `tool_search`
and other unsupported endpoint/tool requests are rejected. Input token counting
uses the local estimator. SIWC bypasses Codex tickets, cookies, fingerprint
headers, plugin transport and BPS/Prism routes. A failed SIWC request never substitutes the same account's Codex credential
or an API key. Existing group-level account failover policies still apply.
Upstream subscription errors remain visible through existing response/error
adapters.

## Admin API

All routes use existing administrator authentication:

- `GET /api/v1/admin/accounts/:id/siwc`: safe binding status and latest SIWC task.
- `POST /api/v1/admin/accounts/:id/siwc/authorize`: create an account-bound browser
  authorization session; returns `auth_url` and `session_id`.
- `POST /api/v1/admin/accounts/:id/siwc/callback`: body `session_id`, `callback_url`;
  validates and stores the grant, without returning tokens.
- `POST /api/v1/admin/accounts/:id/openai-reauth`: body `{"oauth_profile":"siwc"}`;
  authorize with the saved password/TOTP worker.
- Account edit persists `extra.openai_siwc` as a boolean protocol selection.

## Validation and references

Regression cases cover signed ID-token validation, callback state/account/client
binding, one-time session consumption, primary-grant preservation, refresh/401
isolation, SQL compare-and-swap and task ownership, request routing, password/TOTP
callback handling, consent handoff and UI selection. The existing backend,
frontend and packaged-worker CI workflows run these checks. The authorization UI
regression now uses the real API client and response interceptor, mocking only
HTTP. The packaged dependency test executes its actual password, TOTP, workspace
and callback helpers with an offline HTTP transport; it also checks initial
consent, wrong state and rejected passwords. These checks do not establish live
subscriber entitlement or inference availability. An isolated live password/TOTP
attempt on 2026-10-21 reached the browser-interaction handoff; no live SIWC grant
was bound and no inference request was made during that check.

Protocol references, checked 2026-10-20:

- [Registration and sign-in](https://developers.openai.com/siwc/token-sharing-open-source/sign-in)
- [Token reference](https://developers.openai.com/siwc/token-sharing-open-source/token-reference)
- [Models and inference](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference)
- [Preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations)
- [Errors and recovery](https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery)

CPA reference: `router-for-me/CLIProxyAPI`, commit
`7b0319fb1c191cdae1e5936ecb81e812dcf0a0b2`, introduces the same distinct SIWC
OAuth resource and direct Responses route. This implementation follows OpenAI's
published contract and Sub2API's existing encrypted credential/worker design.
