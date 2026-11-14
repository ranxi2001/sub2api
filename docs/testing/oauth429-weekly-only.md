# OAuth 429 weekly-only permanent disable

Baseline: `f895ea743f34a4c4ab6c9d01c01e13c931625a6e`.
Production scope: weekly policy, quota snapshot header provenance, and the three
stream/WS sites that separate permanent-policy evidence from temporary cooldown headers, retained handshake observation time, and persisted quota-query observations (including low/zero recovery).
All public fixtures are fictional. Local work is static only; dynamic evidence is
produced by `.github/workflows/oauth429-weekly-only.yml` on GitHub Actions.

## Historical-source compilation boundary

The service RED worktree starts at the exact candidate SHA and restores only the
13 pinned production blobs listed in the workflow from the baseline above.
`restored-blobs.txt` and `candidate-source-blobs.txt` record both sides. The old
WS implementation intentionally remains unchanged: adding the newer
`openAIWSForwardStreamPhaseContext` helper would alter the counterexample.

Only `backend/internal/service/temp_unsched_pending_test.go` is excluded from
this disposable service RED worktree. It is absent at the baseline and its WS
test references that candidate-only helper; none of its tests or fixtures is a
weekly429 regression dependency. `excluded-tests.txt` records its exact
candidate blob and the reason. The candidate checkout keeps the entire file;
the race-enabled GREEN command and evidence validator require all six of its
top-level regressions to pass, including response counting, pending/credential
401 controls, WS stream phase and delayed counter reset.

All weekly429 service/repository test blobs stay identical to the candidate,
with the full list and hashes in `retained-tests.txt` and
`retained-test-blobs.txt`. No other test modification is allowed in the service
RED worktree. Cache, expiry and PostgreSQL query RED worktrees retain all tests
and their existing source boundaries. A compile/setup failure, skip, panic or
missing target assertion still cannot satisfy the behavioral RED validator.

An opted-in OAuth owner is permanently disabled only on an actual upstream 429
when valid explicitly identified 10080-minute usage is strictly above its own
threshold (default 98). Primary and secondary are judged independently. A 5h,
zero, absent or malformed unrelated neighbor cannot veto valid weekly evidence.
Two weekly windows, duplicate weekly fields, invalid weekly usage/reset, and
unidentified quota observations fail closed without reusing cached high usage.
The percentage is not inferred from a body `usage_limit_reached` or
`limit_window_minutes` field. No maximum across percentages is selected.

When there is no current explicit weekly observation, cache fallback requires
`codex_7d_window_minutes=10080` and retains account identity, percentage,
observation, per-field provenance, staleness and reset checks. If field versions
exist, both usage and window identity must have valid fresh versions from the same
source observation. A partial packet cannot borrow another packet's weekly identity.
Rejected weekly headers carry an observation timestamp through parser/persistence;
a later headerless 429 cannot trust usage from that packet. A newer valid weekly
observation may supply fresh evidence again. Validated weekly usage is also retained as one versioned observation, independently of legacy normalized windows. Nonweekly updates cannot erase it; newer explicit quota-query low/zero usage supersedes it in the same repository write. Temporary quota normalization is unchanged. Legacy
snapshots without an explicit window no longer authorize a permanent disable.
Nonweekly or unidentified headers do not veto independently valid weekly cache;
unidentified percentages themselves never count as weekly evidence.

## Execution coverage

| Test | Boundary exercised |
| --- | --- |
| `TestOAuth429WeeklyOnlyService` | Public RateLimitService → actual policy → spy durable write + existing quota header parser/persistence |
| `TestOAuth429WeeklyOnlyHeaderBoundaries` | Both weekly positions; zero/invalid/absent/5h neighbors; 5h-only 100; missing/low/invalid weekly; duplicates and conflicts; no reuse of high cache |
| `TestOAuth429WeeklyOnlyCachedIdentity` | Explicit identity; global/field freshness; identity mismatch; future/stale/missing versions; absolute/relative resets |
| `TestOAuth429WeeklyOnlyCacheDoesNotRelabelUnidentifiedUsage` | Actual parser/normalizer/merge; partial usage-only and window-only packets cannot combine into weekly evidence |
| `TestOAuth429WeeklyOnlyNonweeklyHeadersAllowExplicitCache` | Public RateLimitService: explicit fresh cached 99 with only 300-minute 100 and zero/invalid neighbor; absent/unidentified cache and equality remain closed |
| `TestOAuth429WeeklyOnlySequentialCache` | Public error service → actual parser → per-field persist/merge → reload → headerless 429; duplicate/conflicting/invalid weekly packets cannot poison cache |
| `TestOAuth429WeeklyOnlyInvalidWeeklyPersistence` | Real PostgreSQL JSONB: NaN/Inf/duplicate weekly headers, prior-high cache, reload, delayed older snapshot, headerless 429 |
| `TestOAuth429WeeklyOnlyNonweeklyPersistenceRetainsEvidence` | Parser/merge and production PostgreSQL variants preserve weekly 99 across subsequent 5h/zero persistence and reload |
| `TestOAuth429WeeklyOnlyQueryUsageSupersedesEvidence` | Actual QueryUsage via loopback HTTP → PostgreSQL update/reload → headerless 429; low/zero supersedes high, delayed observations cannot overwrite newer evidence |
| `TestOAuth429WeeklyOnlyHTTPStreamExpiredObservation` | Real loopback HTTP flushes weekly headers, waits beyond reset, then sends semantic 429 through native/passthrough streamed and buffered paths plus WS HTTP bridge; temporary cooldown retained |
| `TestOAuth429WeeklyOnlyPolicyGates` | Own threshold, strict equality, invalid threshold, opt-out, non429, body-only weekly classification |
| `TestOAuth429WeeklyOnlyHTTPForward` | Actual `Forward` entry, native and OAuth passthrough HTTP paths, mocked transport response, primary/secondary weekly zero-neighbor and negative cases |
| `TestOAuth429WeeklyOnlyWSForward` | Actual `Forward` entry, real loopback WS upstream handshake 429 and error frames, handshake quota, elapsed handshake reset (simulated on an actual connection after peer read), fresh low/invalid versus high cache, and explicit-cache/body-only controls; observes upstream peer and retains event-reset temporary cooldown |
| `TestOAuth429WeeklyOnlyWSClientIngress` | Real loopback client socket → `ProxyResponsesWebSocketFromClient` ctx_pool path → upstream adapter fixture → secondary weekly error frame |
| `TestOAuth429WeeklyOnlyCombinedRotation` | Actual RateLimitService and auto2FA service together, real queue method with spy storage, manual snapshot untouched |
| `TestOAuth429WeeklyOnlyRotationPreservesScheduling` | Isolated PostgreSQL: actual permanent scheduling write, real automatic rotation repository lifecycle success/failure, manual enable/disable after queueing, successful-test recovery |
| Existing scheduling/weekly/managed-owner/TOTP tests selected by workflow | Owner projection fences, per-account opt-in, persistence errors, deduplication, cancellation independence, retry blocking and independent rotation |

These are isolated regression tests, not live production acceptance. The HTTP
transport is mocked, the WS loopback upstream and credentials are fictional, and
no paid calls or production writes occur. The repository lifecycle uses fictional
worker transitions; no real 2FA enrollment runs. The combined service and durable
repository tests are separate boundaries, not a claimed end-to-end worker test.

HTTP response receipt and WS handshake times accompany retained headers through all stream and buffered-event policy paths.
The permanent policy receives original stream/WS headers through context before
temporary cooldown sanitization. Ordinary-model temporary cooldowns still discard
successful handshake quota headers and use their existing event/body policy.

## Caller coverage limits

The three direct entrances to the permanent policy are the common
`handleUpstreamErrorAfterStreakReset`, gateway
`handleOpenAIAccountUpstreamError`, and BPS `coolDownExcelBPS`. The gateway shares
its observation context with the common handler, avoiding duplicate permanent
writes. Native HTTP/passthrough and WS Forward/client ctx_pool have entry-point
execution above. Existing image/BPS/managed/SIWC tests call shared handlers; they
are not full image/BPS transport acceptance. WS passthrough,
images, embeddings, count tokens, model listing, alpha search, chat/messages
compatibility and BPS repair variants are statically mapped below, not all
individually executed by this focused suite.

Generic provider callers below also reach the common service; non-OpenAI or
non-OAuth owners remain excluded by the existing policy. Account connection-test
`reconcileOpenAI429State` (account_test_service.go) is a separate temporary-state
reconciliation path and does not call this permanent policy. This repair does
not extend permanent-disable behavior into that path.

The inventory lists direct production calls to policy/shared error handlers and
HTTP/WS wrapper entry calls. It is a static inventory, not a runtime coverage
report. Line numbers refer to this change's source tree.

| Source | Enclosing caller | Callee |
| --- | --- | --- |
| `antigravity_gateway_retry.go:1279` | `handleUpstreamError` | `HandleUpstreamError` |
| `gateway_anthropic_passthrough.go:228` | `forwardAnthropicAPIKeyPassthroughWithInput` | `handleFailoverSideEffects` |
| `gateway_anthropic_passthrough.go:255` | `forwardAnthropicAPIKeyPassthroughWithInput` | `handleErrorResponse` |
| `gateway_anthropic_passthrough.go:869` | `invalidNonStreamingJSONFailoverError` | `HandleUpstreamError` |
| `gateway_anthropic_passthrough.go:871` | `invalidNonStreamingJSONFailoverError` | `HandleUpstreamError` |
| `gateway_bedrock.go:316` | `handleBedrockUpstreamErrors` | `handleFailoverSideEffects` |
| `gateway_bedrock.go:335` | `handleBedrockUpstreamErrors` | `handleErrorResponse` |
| `gateway_count_tokens.go:228` | `ForwardCountTokens` | `HandleUpstreamError` |
| `gateway_count_tokens.go:332` | `forwardCountTokensAnthropicAPIKeyPassthrough` | `HandleUpstreamError` |
| `gateway_forward.go:739` | `Forward` | `handleFailoverSideEffects` |
| `gateway_forward.go:768` | `Forward` | `handleErrorResponse` |
| `gateway_forward.go:806` | `Forward` | `handleFailoverSideEffects` |
| `gateway_forward.go:810` | `Forward` | `handleErrorResponse` |
| `gateway_forward.go:1046` | `sseStreamErrorEventFailover` | `handleFailoverSideEffects` |
| `gateway_forward_as_chat_completions.go:168` | `ForwardAsChatCompletions` | `HandleUpstreamError` |
| `gateway_forward_as_responses.go:179` | `ForwardAsResponses` | `HandleUpstreamError` |
| `gateway_systemone.go:149` | `handleSystemOneErrorResponse` | `HandleUpstreamError` |
| `gateway_upstream_response.go:474` | `handleErrorResponse` | `HandleUpstreamError` |
| `gateway_upstream_response.go:476` | `handleErrorResponse` | `HandleUpstreamError` |
| `gateway_upstream_response.go:590` | `handleRetryExhaustedSideEffects` | `HandleUpstreamError` |
| `gateway_upstream_response.go:592` | `handleRetryExhaustedSideEffects` | `HandleUpstreamError` |
| `gateway_upstream_response.go:604` | `handleFailoverSideEffects` | `HandleUpstreamError` |
| `gateway_upstream_response.go:607` | `handleFailoverSideEffects` | `HandleUpstreamError` |
| `gemini_messages_compat_service.go:3183` | `handleGeminiUpstreamError` | `HandleUpstreamError` |
| `openai_account_runtime_block_fastpath.go:98` | `handleOpenAIAccountUpstreamError` | `maybeDisableOpenAISchedulingOn429` |
| `openai_account_runtime_block_fastpath.go:233` | `handleOpenAIAccountUpstreamError` | `handleUpstreamErrorAfterStreakReset` |
| `openai_alpha_search.go:104` | `ForwardAlphaSearch` | `handleFailoverSideEffects` |
| `openai_alpha_search.go:183` | `forwardAlphaSearchViaResponsesWebSearch` | `handleFailoverSideEffects` |
| `openai_codex_models_service.go:1921` | `handleCodexModelsManifestAccountAuthError` | `handleOpenAIAccountUpstreamError` |
| `openai_embeddings.go:160` | `ForwardEmbeddings` | `handleOpenAIAccountUpstreamError` |
| `openai_excel_bps.go:36` | `handleExcelBPSUnauthorized` | `handleOpenAIAccountUpstreamError` |
| `openai_excel_bps.go:71` | `handleExcelBPSUnauthorized` | `HandleUpstreamError` |
| `openai_excel_bps.go:283` | `forwardExcelBPS` | `coolDownExcelBPS` |
| `openai_excel_bps.go:668` | `forwardExcelBPS` | `handleOpenAIAccountUpstreamError` |
| `openai_excel_bps.go:775` | `forwardExcelBPS` | `coolDownExcelBPS` |
| `openai_excel_bps.go:819` | `forwardExcelBPS` | `coolDownExcelBPS` |
| `openai_excel_bps.go:924` | `forwardExcelBPS` | `coolDownExcelBPS` |
| `openai_excel_bps_images.go:304` | `forwardExcelBPSImages` | `coolDownExcelBPS` |
| `openai_excel_bps_ratelimit.go:59` | `coolDownExcelBPS` | `maybeDisableOpenAISchedulingOn429` |
| `openai_excel_bps_ratelimit.go:62` | `coolDownExcelBPS` | `handleOpenAIAccountUpstreamError` |
| `openai_gateway_cc_pipeline.go:141` | `failoverOpenAIUpstreamHTTPError` | `handleOpenAIAccountUpstreamError` |
| `openai_gateway_chat_completions.go:708` | `handleChatCompletionsErrorResponse` | `handleCompatErrorResponse` |
| `openai_gateway_count_tokens.go:107` | `ForwardResponsesInputTokens` | `HandleUpstreamError` |
| `openai_gateway_count_tokens.go:352` | `ForwardCountTokensAsAnthropic` | `HandleUpstreamError` |
| `openai_gateway_forward.go:1488` | `Forward` | `handleErrorResponse` |
| `openai_gateway_forward.go:1499` | `Forward` | `handleErrorResponse` |
| `openai_gateway_forward.go:1550` | `Forward` | `handleFailoverSideEffects` |
| `openai_gateway_forward.go:1561` | `Forward` | `handleErrorResponse` |
| `openai_gateway_forward.go:1660` | `Forward` | `handleFailoverSideEffects` |
| `openai_gateway_forward.go:1666` | `Forward` | `handleErrorResponse` |
| `openai_gateway_grok.go:207` | `forwardGrokResponses` | `handleErrorResponse` |
| `openai_gateway_messages.go:641` | `handleAnthropicErrorResponse` | `handleCompatErrorResponse` |
| `openai_gateway_passthrough.go:1024` | `handleFailoverErrorResponsePassthrough` | `handleOpenAIAccountUpstreamError` |
| `openai_gateway_passthrough.go:1097` | `handleErrorResponsePassthrough` | `handleOpenAIAccountUpstreamError` |
| `openai_gateway_passthrough.go:1851` | `handleOpenAIStreamTerminalAccountSideEffects` | `handleOpenAIStreamTerminalAccountSideEffectsWithContext` |
| `openai_gateway_passthrough.go:1886` | `handleOpenAIStreamTerminalAccountSideEffectsWithContext` | `handleOpenAIAccountUpstreamError` |
| `openai_gateway_responses_chat_fallback.go:179` | `forwardResponsesViaRawChatCompletions` | `handleErrorResponse` |
| `openai_gateway_upstream_errors.go:597` | `handleFailoverSideEffects` | `handleOpenAIAccountUpstreamError` |
| `openai_gateway_upstream_errors.go:599` | `handleFailoverSideEffects` | `handleOpenAIAccountUpstreamError` |
| `openai_gateway_upstream_errors.go:713` | `handleErrorResponse` | `handleOpenAIAccountUpstreamError` |
| `openai_gateway_upstream_errors.go:784` | `handleErrorResponse` | `handleOpenAIAccountUpstreamError` |
| `openai_gateway_upstream_errors.go:999` | `handleCompatErrorResponse` | `handleOpenAIAccountUpstreamError` |
| `openai_images.go:798` | `forwardOpenAIImagesAPIKey` | `handleFailoverSideEffects` |
| `openai_images_responses.go:993` | `handleOpenAIImagesErrorResponse` | `handleOpenAIAccountUpstreamError` |
| `openai_images_responses.go:1936` | `forwardOpenAIImagesOAuth` | `handleFailoverSideEffects` |
| `openai_images_responses.go:2125` | `handleOpenAIImagesOAuthResponseError` | `handleOpenAIAccountUpstreamError` |
| `openai_images_responses.go:2198` | `handleOpenAIImagesOAuthResponseError` | `handleOpenAIAccountUpstreamError` |
| `openai_ws_forwarder_ingress.go:1749` | `ProxyResponsesWebSocketFromClient` | `handleOpenAIWSDialTransientFailure` |
| `openai_ws_forwarder_ingress.go:1992` | `ProxyResponsesWebSocketFromClient` | `handleOpenAIWSErrorEventTransientFailure` |
| `openai_ws_forwarder_ingress.go:2160` | `ProxyResponsesWebSocketFromClient` | `handleOpenAIWSTerminalTransientFailure` |
| `openai_ws_forwarder_support.go:325` | `handleOpenAIWSTerminalTransientFailure` | `handleOpenAIWSFailureAccountSideEffects` |
| `openai_ws_forwarder_support.go:341` | `handleOpenAIWSErrorEventTransientFailure` | `handleOpenAIAccountUpstreamError` |
| `openai_ws_forwarder_support.go:354` | `handleOpenAIWSFailureAccountSideEffects` | `handleOpenAIStreamTerminalAccountSideEffectsWithContext` |
| `openai_ws_forwarder_support.go:360` | `handleOpenAIWSFailureAccountSideEffects` | `handleOpenAIStreamTerminalAccountSideEffectsWithContext` |
| `openai_ws_forwarder_support.go:368` | `handleOpenAIWSFailureAccountSideEffects` | `handleOpenAIAccountUpstreamError` |
| `openai_ws_forwarder_support.go:380` | `handleOpenAIWSDialTransientFailure` | `handleOpenAIAccountUpstreamError` |
| `openai_ws_forwarder_support.go:723` | `persistOpenAIWSRateLimitSignal` | `handleOpenAIAccountUpstreamError` |
| `openai_ws_forwarder_v2.go:379` | `forwardOpenAIWSV2` | `handleOpenAIWSDialTransientFailure` |
| `openai_ws_forwarder_v2.go:958` | `forwardOpenAIWSV2` | `handleOpenAIWSErrorEventTransientFailure` |
| `openai_ws_forwarder_v2.go:1085` | `forwardOpenAIWSV2` | `handleOpenAIWSTerminalTransientFailure` |
| `openai_ws_http_bridge.go:696` | `proxyOpenAIWSHTTPBridgeTurn` | `handleOpenAIAccountUpstreamError` |
| `openai_ws_http_bridge.go:817` | `proxyOpenAIWSHTTPBridgeTurn` | `handleOpenAIWSFailureAccountSideEffects` |
| `openai_ws_http_bridge.go:981` | `proxyOpenAIWSHTTPBridgeTurn` | `handleOpenAIWSFailureAccountSideEffects` |
| `openai_ws_http_bridge.go:1089` | `proxyOpenAIWSHTTPBridgeTurn` | `handleOpenAIWSTerminalTransientFailure` |
| `openai_ws_v2_passthrough_adapter.go:981` | `proxyResponsesWebSocketV2Passthrough` | `handleOpenAIWSDialTransientFailure` |
| `openai_ws_v2_passthrough_adapter.go:1597` | `proxyResponsesWebSocketV2Passthrough` | `handleOpenAIWSFailureAccountSideEffects` |
| `ratelimit_service.go:354` | `HandleUpstreamError` | `handleUpstreamErrorAfterStreakReset` |
| `ratelimit_service.go:360` | `handleUpstreamErrorAfterStreakReset` | `maybeDisableOpenAISchedulingOn429` |
