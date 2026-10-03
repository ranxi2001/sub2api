# Grok 403 scheduling policy

Grok OAuth account groups are not API-key `pool_mode`. A shared upstream
refusal can affect multiple accounts, so enabling unrestricted failover while
quarantining each selected account can exhaust a group.

## Account setting

The Grok account editor exposes **Keep scheduling on unclassified xAI 403**.
It persists `extra.grok_skip_forbidden_pause` as a boolean. Missing, invalid,
or false values retain the legacy default behavior; this is an opt-in change.
Saving the setting preserves unrelated account extras and credentials.

When enabled, an unclassified inference HTTP 403:

- Does not install the default 30-minute whole-account scheduling pause,
  account-wide quota-header limit, or scheduling-threshold utilization.
- Allows at most one additional upstream account attempt per request after
  the first such refusal. A different error on that alternate does not reset
  the budget. Existing global retry limits can stop the request sooner.
- Is excluded from account-health/EWMA failure observations. Request errors,
  upstream status, and normal operations diagnostics are still recorded.
- Does not permit replay after semantic stream output or an accepted realtime
  connection. Exhaustion uses the existing endpoint-specific error response.

The setting is not a blanket ignore-403 switch. Explicit credential/account
access failures, subscription/entitlement denial, billing and quota failures,
and administrator keyword cooldown rules retain their protection. Explicit
content-policy refusals terminate the request without account rotation or
health penalties, even if their message includes words such as "please retry".

The same account-state classification applies to manual and scheduled account
tests. Realtime handshake classification uses the bounded upstream error body,
not the exception's display string.

## Quota and recovery scope

A free-usage error explicitly naming one model installs a model block, not an
account-wide snapshot limit or utilization threshold. Other models remain
eligible. Valid future reset times, including short reset times, are retained
instead of being expanded to the two-hour fallback.

Explicit administrator rate-limit or temporary-state clearing removes the
account's process-local model blocks and its team's process-local model
overlay. Because team limits are shared, resetting that team overlay also
affects sibling accounts in the same team; unrelated teams are not touched.
The account-recovery endpoint also recognizes hidden scoped state.

Successful test recovery does not prove which model was tested. It therefore
preserves Grok model/team blocks and durable model limits while retaining the
existing account-level recovery behavior. OAuth refresh success only clears
recognized credential-401 temporary state, not a quota or inference-403 pause.

## Operational boundaries

- The switch does not fix the upstream refusal or prove its cause. A missing
  response body must remain unclassified, not be labeled as a content refusal,
  subscription failure, or an IP ban.
- OAuth inference normally uses the CLI proxy, while API-key traffic uses the
  public API. Check the resolved endpoint: an explicit per-account `base_url`
  remains pinned even when the global default is CLI mode. Do not migrate
  endpoints or credentials solely from the HTTP status.
- Model and team overlays remain process-local. This change does not add
  cross-replica synchronization or a provider/egress circuit breaker. Repeated
  independent requests can still encounter the same upstream rejection.
- Refresh recovery rereads the current reason before clearing it. The existing
  repository/cache interfaces do not provide an atomic reason/generation CAS;
  a new concurrent block between that read and clear is a remaining race.
- No database migration is required. Binary rollback leaves the account extra
  in place; older binaries without the setting may ignore it.

Diagnostics and public reproductions must not contain tokens, raw prompts,
account exports, proxy credentials, or full production configuration. Test
fixtures should use synthetic accounts and mocked upstream responses.
