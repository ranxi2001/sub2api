# OpenAI authorization connections

Refs #594. The implementation was merged in [PR #595](https://github.com/ranxi2001/sub2api/pull/595); [PR #601](https://github.com/ranxi2001/sub2api/pull/601) subsequently restricted management to OAuth parent accounts and new registrations to Codex/BPS/SIWC. This is a source/CI contract, not a statement of production availability or OpenAI commercial approval.

An existing `accounts.id` remains the local account identity and the owner of concurrency, security restrictions, model/group policy, shared quota and billing. Its authorization connections have their own UUIDs, issuer, client, validated subject, workspace when available, resource/audience, scopes, host, encrypted token set, revision and lifecycle. Application-user `auth_identities` and identity channels remain sign-in records; they do not confer inference authorization. Email is a recognition hint, never a connection or identity merge key. A SIWC subject may be pairwise for its client and must not be assumed equal to a Codex/BPS subject.

## Management

Open an eligible OpenAI OAuth parent account in account management. The connection panel lists registrations, status and granted capabilities. Add a Codex, BPS or SIWC connection, give it a distinct label, and complete the selected registration's OAuth consent. New registrations remain pending until their ID token is verified. New `api_key` registrations and OAuth registrations carrying an `api_key` are rejected. Credentials stay encrypted on the server and are never returned by the connection API. Previously saved ID tokens are not embedded in authorization URLs returned to the panel.

API key accounts have no authorization-connection panel and cannot use these management APIs, even if their stored credentials also contain OAuth fields. Their existing account credential controls and inference routes remain available. Shadow accounts and other platforms are also excluded from connection management.

Select a capable connection explicitly. Selection activates managed routing; adding a pending registration alone preserves the existing route. A disabled, unlinked, cooling or identity-only selected registration fails closed. The router does not silently inherit the original Codex token or choose another client's token. A later explicit selection applies to new sessions; active sessions keep their original connection until their one-hour affinity expires. If that connection becomes unusable, reconnect with a new session after choosing the appropriate grant. Session IDs sent upstream are separated by tenant and connection identity.

Reauthorization keeps the connection ID, issued client, SIWC host and validated subject; non-SIWC workspace changes are rejected. Disable/enable affects one connection. Revoke/unlink clears only the selected connection locally; OAuth remote revocation uses discovery's HTTPS OpenAI revocation endpoint and its actual client. The API/UI distinguishes confirmed remote revocation from a local-only unlink. If unconfirmed, disconnect the app in ChatGPT Settings or revoke the API key in Platform. Never infer revocation from successful local token deletion.

Historical managed `api_key` connections remain readable by the inference path, including records attached to API key parent accounts. For a historical managed `api_key` connection under an eligible OAuth parent, the retained replacement control and API can update that connection's key; replacement does not revoke the old key at Platform. This compatibility path cannot create a new API key connection. API key parents cannot use the replacement or other authorization-connection management endpoints.

SIWC identity consent alone gives no inference capability. ChatGPT plan inference uses `https://api.openai.com/v1/responses`, `stream:true`, `store:false`, existing SSE terminal-event/error handling and SIWC header restrictions. Codex and BPS use their existing dedicated adapters with their actual registration clients. An unknown type, a `swic` typo, an identity-only grant or an unsupported endpoint is rejected. New managed routes are HTTP only; native WebSocket, image-generation and other unimplemented capabilities are not advertised. Existing routes retain their prior behavior. Historical managed Platform API key connections use the official Platform endpoint; custom compatible providers remain on their existing account configuration.

Paid or remotely hosted SIWC applications require OpenAI approval. The application must not imply that successful local binding grants that approval. OAuth security challenges and consent require normal human interaction. Offline CI uses only synthetic signed identities and loopback network responses and cannot prove account eligibility, commercial approval or production inference.

## API

All routes use the existing administrator account-management authorization boundary and require an eligible OpenAI OAuth parent account. An `account.type=apikey` parent is rejected by management APIs; historical managed API key inference uses separate internal readers.

| Method | Route below `/api/v1/admin/accounts/:id/auth-connections` | Input / result |
| --- | --- | --- |
| GET | empty | Safe connection list, including read-only legacy mappings |
| POST | empty | `kind`: `codex`, `bps` or `siwc`, plus `label`; rejects `api_key` credentials and kind; returns safe registration |
| PATCH | `/:connection_id` | Current `revision`, `action`: `select`, `disable`, `enable`, `unlink`, `recover` |
| POST | `/:connection_id/authorize` | Starts the bound OAuth flow; returns session and browser URL |
| POST | `/:connection_id/callback` | `session_id`, complete `callback_url`; consumes state/nonce/PKCE once |
| PUT | `/:connection_id/api-key` | Compatibility only: current `revision` and replacement `api_key` for a historical managed API key connection under an OAuth parent; cannot create a connection or update a read-only legacy mapping |
| POST | `/:connection_id/revoke` | Current `revision`; returns `remote_revocation_confirmed` |
| POST | `/use-legacy` | Explicitly restore the existing account route without deleting connections |

Stale credential/status revisions return 409. Selection is an account policy change and does not alter token revisions. Pending authorization sessions are bound to connection, account, kind and revision and use the existing encrypted shared session store. In a production multi-instance setup, all replicas must share that Redis and the existing durable credential-encryption key. PostgreSQL leases serialize refresh, callback exchange and revocation for each connection; owner-checked release prevents another process's lease from being removed. Token replacement uses revision CAS. A connection refresh is bounded below the lease lifetime and persists rotating credentials with a bounded detached context.

Credential failures/cooldowns are attributed to the connection. Explicit account/workspace unavailability and 429 keep existing account-level restrictions; unknown entitlement sharing is restricted at the account rather than treated as extra quota. Accounting stays on the same account: the connection usage table adds observational request attribution and does not change financial data or arithmetic. Error events carry connection IDs without tokens. New connections do not create additional scheduler rows or concurrency capacity.

## Upgrade and rollback

Migrations 312–317 add connection records, selection policy, scheduler metadata/outbox triggers, observational usage and session affinity. They are forward-only and do not rewrite existing token material. Reapplying the migration runner is safe under its checksum rules. Existing Codex/API key slots and independent BPS/SIWC side tables have stable read-only mapping UUIDs, computed from account ID and storage slot. Eligible OAuth parents can list their mappings; API key parents retain internal runtime compatibility without access to the connection-management API. No rotating token is copied into a second owner. Legacy mappings make no new signature-verification claim; eligible OAuth accounts can authorize a new managed OAuth connection to obtain that guarantee.

Old clients keep their existing account APIs. Server-owned connection metadata in `accounts.extra` survives old-client whole-map writes. Managed credentials can only be persisted through the connection repository CAS. On eligible OAuth parents, selecting “Use existing account route” disables managed selection and leaves new credentials stored. Before a binary rollback, explicitly restore a supported legacy route, ensure no new sessions depend on managed registrations, and retain the new tables and encryption key. Historical managed API key parents need a separate rollback plan because `/use-legacy` also rejects these parents. Older binaries cannot read managed token sets, new connection metadata or their pending callbacks. A binary downgrade does not undo migrations and must not drop connection data. Do not mix old/new gateway binaries while managed routing is enabled: old binaries do not enforce its selection policy.

## Verification boundaries

Dedicated `openai-multi-auth.yml` checks the original dispatch RED before exact-head GREEN, compiles real adapters, and executes HTTP/service/repository/PostgreSQL integration in containers isolated on GitHub runners. OAuth tests replace only the last network boundary and validate signed synthetic ID tokens using the actual verifier. Gateway tests use real request construction and SSE processing against an isolated HTTP server. UI tests use actual API clients; screenshots render the actual component/styles in zh/en with synthetic HTTP fixtures. Raw results and exact-head status must be checked in the PR; this file does not assert that a pending job passed.

Official references: [sessions](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions), [inference](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference), [token reference](https://developers.openai.com/siwc/token-sharing-open-source/token-reference), [overview and hosted-app access](https://developers.openai.com/siwc/token-sharing-open-source).


## Blocking-review correction

Two Hermes source reviews of fixed `28a66fc47` completed with blocking findings. The former 35-green checkpoint does not prove their closure. The subsequent remediation and validation history are recorded in merged [PR #595](https://github.com/ranxi2001/sub2api/pull/595) and PROGRESS.md; those historical results do not establish checks for a later source revision.

Managed BPS advertises only native Responses; Chat Completions/Messages are rejected until a real bridge is implemented. Managed Codex never imports parent ticket/cache/harvest/Cookie/session state, even if a legacy ticket has no identity. Models subject to the existing ticket gate fail closed for these registrations; no new session acquisition or approval is implied. Client and credential revision changes invalidate any previous connection identity.

Before rotating refresh, the connection durably records an in-progress attempt. A definitely pre-send failure may restore idle. Explicit OAuth rejection requires authorization; timeout, verification, encryption or uncertain persistence leaves inference closed and never retries the old rotating token. A verified replacement is first journaled encrypted and then atomically finalized under owner/epoch/expiry fencing. “Recover saved refresh” finalizes that saved replacement without another upstream POST. If no durable replacement exists, reauthorization is required. Disabled/unlinked registrations cannot be revived by recovery. Every attachment and catalog network attempt rechecks live connection state/revision after waits.


Lease acquisition returns its owner and epoch in the same PostgreSQL UPDATE RETURNING. Each lifecycle call retains that proof; later reads may verify it but cannot adopt another owner's proof after expiry. SQL still checks the acquired proof and expiry at every commit. Active staged replacements can be finalized after restarting the service without another token POST. If an administrator disables a connection with an unresolved candidate, recovery and enable remain blocked: the encrypted journal stays quarantined until explicit reauthorization or unlink, and recovery never reactivates the connection.
