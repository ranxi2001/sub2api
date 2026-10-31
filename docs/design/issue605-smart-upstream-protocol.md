# Issue 605: conservative session upstream routing

Refs #605 and https://github.com/ranxi2001/sub2api/issues/605#issuecomment-6528507177 . Original baseline: 77455cf755fa0f119367b911704f11df88e7917e. Current production baseline: dc3d41441ba914b25d7f26c49618c25a1df99e77 (#602, integrated by ordinary merge; no rebase). This is a Draft implementation/acceptance record, not a performance claim.

## Contract and adapter evidence

The downstream remains WebSocket. Select once using the first create payload and the actual selected authorization, then keep that upstream protocol until disconnect. HTTP-first continuation is HTTP-to-HTTP, never permission to migrate a WS chain. No mid-session migration, timeout fallback, replay after submission, paid probes or A/B traffic.

| Authorization / adapter | HTTP evidence | WS evidence / restriction | Smart eligibility |
| --- | --- | --- | --- |
| API key, native Responses | Existing successful Responses probe `openai_responses_supported=true`; negative and missing differ; storage semantics must remain intact | Existing protocol resolver and account WS mode | Both must be positively supported; explicit store=true on every turn and no stateless CN adapter |
| Native Codex OAuth | Native HTTP Responses request builder exists; OAuth does not imply WS-only | Existing resolver | HTTP chain unconfirmed: native compatibility forces store=false and ordinary HTTP forward removes previous ID; retain WS without claiming WS-only |
| Managed explicit connection | `prepareAuthConnection` selects exact ID/revision; `Supports("responses")` is endpoint capability | Projection currently disables WS mode | Preserve current restriction; never inherit another connection's capabilities |
| BPS / SIWC / Prism / Copilot / plugin | Specialized HTTP adapters do not establish native bridge compatibility | Existing restrictions | No new routing through specialized adapters |
| Unknown / identity-only | No positive HTTP evidence | Existing route only, if otherwise valid | Never infer HTTP support |

`SupportsOpenAIEndpointCapability` deliberately treats some unobserved legacy accounts as schedulable; that is not positive HTTP evidence for smart selection. Endpoint support and stateful HTTP chain support are distinct. Smart HTTP requires explicit store=true on every turn; absence or false remains WS at session start, and incompatible later frames fail closed. The router never enables durable upstream storage on behalf of a client that did not request it. Native Codex is HTTP capable, but this revision intentionally does not promote it to HTTP session routing because its adapter is stateless. The selected authorization identity is part of history and continuation ownership. No account override is implemented in this first version: no unmanaged `extra` option, no UI/API claim. Global configuration supplies manual controls.

## Configuration and compatibility

`gateway.openai_ws.smart_protocol`: `enabled=false`, `mode=auto|shadow|http|ws`, `large_output_threshold=2000`, `long_context_bytes=200000`. Explicit output caps and high effort with long input are heuristics, never empirical p90. Missing configuration retains defaults. Invalid modes/negative thresholds fail validation.

The existing `http_bridge_enabled` remains the gate for smart HTTP selection and the payload compatibility bridge; pre-existing forced platform/plugin/account HTTP routes retain their previous independent behavior. Existing forced platform/plugin/account HTTP bridge and the 15 MiB compatibility path retain precedence. Disabled and shadow preserve existing routing and retry behavior. Active auto/manual also prohibit submitted-request retries on a legacy bridge, while preserving its stateless body/history compatibility. Manual `ws` suppresses smart HTTP decisions but retains explicitly configured legacy bridges. Set `enabled=false` to kill new decisions; existing sessions keep their chosen protocol. Config changes apply to new connections only. In-flight requests are never resent as a rollback mechanism.

## Prediction and observations

Reuse successful turn usage rather than sending samples. Keep a process-local bounded 15-minute window keyed by account, authorization identity/revision, mapped model, effort and actual upstream protocol. Require 20 samples for empirical nearest-rank p90; cold start is explicitly heuristic/unknown. Bound buckets and samples; no timer, new background worker, persistent prompt data or unbounded label cardinality. Restart loses history and returns to cold start. No quality ratio, feedback sampling, client profiling, account override, region inference or performance proof in this version.

Ingress protocol, actual upstream protocol, decision reason, candidate protocol and prediction source must remain separate. First upstream event measures a lifecycle event; first actual content uses an independent strict nonempty content classifier; the legacy FirstTokenMs and its event-type classifier are unchanged. Neither missing measurement nor absent legacy metadata means zero. Reuse optional request timing persistence for the decision; old rows remain absent without migration. Logs use only enums, numeric IDs/counts and durations, never bodies, model strings supplied by users, URLs, credentials or email.

## Guard and risk review

HTTP bridge already performs `admitOpenAITurn` and ticket validation for every request, then `doOpenAIUpstream` performs authorization fencing, RPM, regional billing, priority and safety guards at the final send boundary. WS performs admission/binding validation, RPM, regional and priority guards before write. Handler hooks own API-key/account slots, revalidation, frozen pricing and usage completion. Preserve these hooks and verify actual packages compile.

The legacy bridge strips previous IDs and rebuilds input. Smart HTTP must preserve the original previous ID, reject foreign continuation IDs and retain the same account/authorization identity. Smart requests must bypass rejected-field resubmission and first-output failover. No output is not evidence of no execution or billing. Partial/failed streams must still retain observed usage and release resources.

| Risk / acceptance | Planned real test | Current status |
| --- | --- | --- |
| HTTP-first multi-turn, previous ID preserved, tools streamed | `TestSmartProtocolHTTPFirstSession` real WS ingress + HTTP SSE upstream | Pending |
| WS-first, WS-only/unknown, manual, shadow, legacy 15 MiB | `TestSmartProtocol*` decision + real network sessions | Pending |
| Sticky despite request/config changes; concurrent independent sessions | Real network multi-turn/race suite | Pending |
| No duplicate execution on HTTP/WS errors, EOF, cancellation | Real transport counters, resource cleanup | Pending |
| Account/authorization and continuation ownership, send-time revocation | Real service/handler admission regressions + isolated PG/Redis | Pending |
| Billing/pricing/concurrency/usage readback | Actual handler and repository suites with isolated PG/Redis | Pending |
| Prediction dimensions, cold start, bounds and sample minimum | History regression tests, timing persistence readback | Pending |
| Actual service/handler/repository compilation | Package-based remote Go tests; file-list substitutes prohibited | Pending |
| Production latency/cache/quality goals | No authorized production or paid traffic | Not implemented / not claimed |

All dynamic acceptance runs exclusively in disposable GitHub Actions environments with synthetic loopback HTTP/WS/SSE and isolated PostgreSQL/Redis. Local work is limited to source inspection, formatting and diff checks. Final evidence must bind every check to exact head, include paginated check runs and legacy statuses, preserve run URLs and sanitized logs, and reject skipped/no-test results as acceptance.

## Implementation refinements pending exact-head acceptance

The real Codex adapter review narrowed eligibility: native Codex HTTP is supported, but its stateless normalization is not evidence of a stateful HTTP chain. Smart HTTP therefore requires an API-key native Responses adapter, a positive existing endpoint probe, explicit client store=true, and no stateless CN adapter. It rejects a later store=false/omitted frame rather than enabling upstream retention for the client. This restriction applies to manual HTTP too. Disabled/shadow preserve forced/15 MiB bridge behavior. With auto/manual enabled, legacy bridges keep their body/history compatibility but also prohibit retry after submission; no-replay and stateful HTTP continuation are separate policies.

Optional `ws_turns` in the existing timing JSON keeps at most 128 per-turn observations; first upstream event and first actual content have separate nullable fields, including passthrough. Usage availability remains nullable for shadow/legacy paths that did not collect it. There is no schema migration. Strict smart sessions retain authoritative usage if a stream later fails; the handler settles it once through existing pricing/billing and cyber-policy deduplication. Missing usage remains unavailable, never a synthetic zero.

An earlier remote source (`eb2b448c2f379504c0390b7908605e88db46382d`, run 41096273246) compiled actual service/handler/repository/timing packages: 158 selected tests and 29 isolated repository integration tests passed, none skipped. This does not certify the subsequent implementation. Source 923ffe9aa failed two failure-terminal client lifecycle assertions; the test client was corrected to close after response.failed. Later heads still require the complete package, repeated network, PG/Redis and billing acceptance on their exact SHA.


## Issue600 shared interface and integration boundary

Issue600 is owned by a separate writer. This branch neither edits its worktree nor imports its unmerged fix. Shared source seams are `openai_ws_forwarder_ingress.go`, `openai_ws_http_bridge.go`, `openai_ws_v2_passthrough_adapter.go` and `openai_ws_v2/passthrough_relay.go`.

The integration contract is an immutable original socket frame/client identity snapshot distinct from the account-scoped/model-mapped outbound payload. Ingress `firstClientMessage` and each raw follow-up frame are input evidence, never replaced by a projected upstream body. Current `openAIWSClientPayload.payloadRaw` is projected outbound data; `accountIdentitySourceRaw` is a pre-account-projection snapshot used for retry construction, not a guarantee of pristine socket bytes. Do not substitute either for Issue600's immutable inbound identity interface. The bridge `payload` argument and passthrough relay's outbound frame remain transport payloads, not client identity authorities.

Preserve `OpenAIWSIngressHooks.BeforePayloadParse(turn, raw, effectiveModel)` for original-frame permission preflight and the existing per-turn `BeforeRequest` / `BeforeTurn` admission and pricing locations. Do not move `BeforeRequest` wholesale to solve identity extraction: it also carries policy, model, billing and concurrency responsibilities. Smart routing copies the hook structure and wraps only `AfterTurn`; it neither derives client identity from new timing fields nor rewrites the inbound frame. Future production-base integration must retain Issue600's immutable identity fix together with all these admissions on native, HTTP bridge and passthrough paths. That independent integration review remains pending; this PR does not claim to implement Issue600.

## Monitor gates and remote evidence requirements

- Nonempty content: separate `FirstActualContentMs` / kind (`visible_text`, `reasoning_text`, `tool_input`, `media`) from first upstream event and legacy `FirstTokenMs`. Empty/null/object or unknown metadata deltas and encrypted reasoning alone do not count. Reasoning/tool input are semantic content, not a claim of first visible text. `TestSmartProtocolContentClassification` plus `ActualContentNetwork` exercise HTTP, native WS and passthrough; pending exact candidate execution.
- Passthrough: `PassthroughNetwork`, `PassthroughNoReplay` and the content network family must actually execute. Timing and cancellation/EOF/failure counters cannot be certified by ctx_pool tests.
- Legacy: prior evidence only located the 10s client read assertion. Increasing a deadline is not a proven root-cause analysis. The full 17 MiB test now submits exactly once to a real loopback HTTP/SSE server, retains payload/terminal/usage assertions, and emits bounded stage durations. The identical test and probe run against full candidate and full dc3d41441 baseline service packages; only test files are transplanted. This is package integration, never a file-list replacement. All results remain pending.
- CI proof: whitelist compiler diagnostics, error classes, race/panic and source locations; retain per-phase JSON evidence and require every named smart family, all six actual packages, three network rounds, both new PG/Redis integrations and both legacy lifecycles. No skip/no-test substitution is accepted.
- Model disclosure: rollout confirms this sole Codex CLI writer is `gpt-6.1-sol`, reasoning `xhigh`. Parent Hermes model/configuration remains unknown. No code subagents were started.


## Updated production integration and source self-review

Parent authorized integrating production after #602 merged at `dc3d41441ba914b25d7f26c49618c25a1df99e77`. Ordinary merge `ffdedceabf016d897aa5330ad8c8c0124bcf613e` has parents `b633ca92597b44126ecba5a6f152e547e8a1b465` and that production SHA; no conflicts, rebase or force push. #602 account-connection timing remains unchanged. In the only common collector file, its injectable clock and event timing coexist with this branch's optional protocol/turn fields and independent snapshot copies. Explicit `TestAccountConnectionTimingLifecycle` and `TestAccountConnectionTimingClockAndPrivacy` are required in the remote actual-package run. The identical legacy package comparison now uses this new production base.

Source self-review focused on the shared merge and changed request/measurement/settlement boundaries: disabled/shadow preserve legacy routing; active decisions stay session-local; stateful HTTP retains same-protocol IDs and storage intent; strict submitted attempts cannot replay; real guards and handler slot/pricing hooks remain on send; authoritative partial usage reaches settlement once; missing usage remains unknown; new content measurements are separate from legacy first-token semantics. Synthetic DB fixtures now clean only their own rows and final HTTP transport is restricted to its loopback fixture host. These are source observations, not dynamic proof. Exact-head execution of all required families, repeated networks, PG/Redis and legacy base/head comparison remains pending.

PR #607's nested isolation is independently owned. No code is copied from that branch or its worktree. Future integration must preserve its original identity checks, the immutable inbound interface described above, and existing admission locations. Parent remains the sole PR merge owner; independent parent source review and final authorization remain outstanding even after CI succeeds.

## P1 repair reopened after independent review

Independent review of 19a441a79 is BLOCKED/P1: passthrough retains authoritative nonterminal usage but EOF/control cancellation/drain expiry can call `AfterTurn` with nil. Old green tests did not cover this lost settlement and do not accept that head. Mandatory repair requires exact-source remote RED, then current-turn-only production result handoff and authenticated handler/PG/Redis GREEN; no aggregate prior-turn rebilling, no terminal double charge or invented usage. `TestSmartProtocolPassthroughPartialUsage` and `TestSmartProtocolPassthroughPartialBilling` cover EOF, client/control cancellation, drain expiry and a previously completed turn, with normal terminal/error/missing-usage controls.

Shared functions remain `ProxyResponsesWebSocketFromClient`, `proxyOpenAIWSHTTPBridgeTurn`, `proxyResponsesWebSocketV2Passthrough`, and WS-v2 `Relay`/its usage-result boundary. The P1 change must not project or mutate raw incoming identity, move `BeforeRequest` wholesale, or weaken any admission. Issue600 writer sa-0-d635b396 and #607 remain independent; no neighbor code is accessed/copied. Future production integration must preserve the immutable original inbound frame/identity snapshot separately from outbound namespace projection and retain all existing checks.


P1 intended behavioral RED is confirmed in run 41117560675/job124275621382: production source-head 19a441a79, tests102fc0631, production blob equality verified. Eight service cases fail P1_CURRENT_TURN_USAGE_LOST; eight authenticated handler PG/Redis cases fail P1_AUTHORITATIVE_USAGE_NOT_BILLED; missing usage, normal terminal and terminal-error controls pass. Two earlier attempts were explicitly rejected (compile adaptation, then global-mode fixture accidentally selecting ctx_pool), never claimed RED. The latter also exposed inaccurate prior passthrough test routing; explicit account passthrough mode is now set.

Focused product repair: WS-v2 Relay snapshots only its unfinished current turn after reader join and existing pending-error settlement. Its optional UnfinishedTurn is separate from aggregate RelayResult.Usage and never an invented protocol terminal or completion callback. Native adapter forwards this snapshot through activeTurn.Swap(0) once on EOF, drain/client close, context cancellation or preemption; ObservedUsageOnError is true only for actually parsed usage, streamReadIncomplete prevents success attribution. Previous terminal turns remain consumed and cannot be rebilled. Disabled/shadow retain earlier semantics. Raw client frames/identity projection, BeforeRequest/BeforeTurn/admissions and terminal-error contract are untouched. Actual GREEN and complete new exact-head acceptance remain pending.
