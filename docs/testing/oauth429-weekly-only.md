# OAuth 429 weekly-only permanent disable

Baseline: `f895ea743f34a4c4ab6c9d01c01e13c931625a6e`.
Only production change: `backend/internal/service/openai_disable_scheduling_429.go`.
All public fixtures are fictional. Local work is static only; dynamic evidence is
produced by `.github/workflows/oauth429-weekly-only.yml` on GitHub Actions.

An opted-in OAuth owner is permanently disabled only on an actual upstream 429
when valid explicitly identified 10080-minute usage is strictly above its own
threshold (default 98). Primary and secondary are judged independently. A 5h,
zero, absent or malformed unrelated neighbor cannot veto valid weekly evidence.
Two weekly windows, duplicate weekly fields, invalid weekly usage/reset, and
unidentified quota observations fail closed without reusing cached high usage.
The percentage is not inferred from a body `usage_limit_reached` or
`limit_window_minutes` field. No maximum across percentages is selected.

When there are no current quota observation headers, cache fallback requires
`codex_7d_window_minutes=10080` and retains account identity, percentage,
observation, per-field provenance, staleness and reset checks. If field versions
exist, both usage and window identity must have valid fresh versions. Legacy
snapshots without an explicit window no longer authorize a permanent disable.
Current unclassified/short-only quota headers continue to block cache fallback.

## Execution coverage

| Test | Boundary exercised |
| --- | --- |
| `TestOAuth429WeeklyOnlyService` | Public RateLimitService → actual policy → spy durable write + existing quota header parser/persistence |
| `TestOAuth429WeeklyOnlyHeaderBoundaries` | Both weekly positions; zero/invalid/absent/5h neighbors; 5h-only 100; missing/low/invalid weekly; duplicates and conflicts; no reuse of high cache |
| `TestOAuth429WeeklyOnlyCachedIdentity` | Explicit identity; global/field freshness; identity mismatch; future/stale/missing versions; absolute/relative resets |
| `TestOAuth429WeeklyOnlyPolicyGates` | Own threshold, strict equality, invalid threshold, opt-out, non429, body-only weekly classification |
| `TestOAuth429WeeklyOnlyHTTPForward` | Actual `Forward` entry, native and OAuth passthrough HTTP paths, mocked transport response, primary/secondary weekly zero-neighbor and negative cases |
| `TestOAuth429WeeklyOnlyWSForward` | Actual `Forward` entry, real loopback WS upstream handshake 429 and error frames, handshake quota and explicit-cache/body-only controls |
| `TestOAuth429WeeklyOnlyWSClientIngress` | Real loopback client socket → `ProxyResponsesWebSocketFromClient` ctx_pool path → upstream adapter fixture → secondary weekly error frame |
| `TestOAuth429WeeklyOnlyCombinedRotation` | Actual RateLimitService and auto2FA service together, real queue method with spy storage, manual snapshot untouched |
| `TestOAuth429WeeklyOnlyRotationPreservesScheduling` | Isolated PostgreSQL: actual permanent scheduling write, real automatic rotation repository lifecycle success/failure, manual enable/disable after queueing, successful-test recovery |
| Existing scheduling/weekly/managed-owner/TOTP tests selected by workflow | Owner projection fences, per-account opt-in, persistence errors, deduplication, cancellation independence, retry blocking and independent rotation |

These are isolated regression tests, not live production acceptance. The HTTP
transport is mocked, the WS loopback upstream and credentials are fictional, and
no paid calls or production writes occur. The repository lifecycle uses fictional
worker transitions; no real 2FA enrollment runs. The combined service and durable
repository tests are separate boundaries, not a claimed end-to-end worker test.

## Caller coverage limits

The three direct entrances to the permanent policy are the common
`handleUpstreamErrorAfterStreakReset`, gateway
`handleOpenAIAccountUpstreamError`, and BPS `coolDownExcelBPS`. The gateway shares
its observation context with the common handler, avoiding duplicate permanent
writes. Native HTTP/passthrough and WS Forward/client ctx_pool have entry-point
execution above. Existing image/BPS/managed/SIWC tests call shared handlers; they
are not full image/BPS transport acceptance. WS passthrough and HTTP bridge,
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
| `openai_gateway_passthrough.go:1883` | `handleOpenAIStreamTerminalAccountSideEffects` | `handleOpenAIAccountUpstreamError` |
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
| `openai_ws_forwarder_support.go:340` | `handleOpenAIWSErrorEventTransientFailure` | `handleOpenAIAccountUpstreamError` |
| `openai_ws_forwarder_support.go:367` | `handleOpenAIWSFailureAccountSideEffects` | `handleOpenAIAccountUpstreamError` |
| `openai_ws_forwarder_support.go:379` | `handleOpenAIWSDialTransientFailure` | `handleOpenAIAccountUpstreamError` |
| `openai_ws_forwarder_support.go:721` | `persistOpenAIWSRateLimitSignal` | `handleOpenAIAccountUpstreamError` |
| `openai_ws_forwarder_v2.go:379` | `forwardOpenAIWSV2` | `handleOpenAIWSDialTransientFailure` |
| `openai_ws_forwarder_v2.go:958` | `forwardOpenAIWSV2` | `handleOpenAIWSErrorEventTransientFailure` |
| `openai_ws_forwarder_v2.go:1085` | `forwardOpenAIWSV2` | `handleOpenAIWSTerminalTransientFailure` |
| `openai_ws_http_bridge.go:696` | `proxyOpenAIWSHTTPBridgeTurn` | `handleOpenAIAccountUpstreamError` |
| `openai_ws_http_bridge.go:816` | `proxyOpenAIWSHTTPBridgeTurn` | `handleOpenAIWSFailureAccountSideEffects` |
| `openai_ws_http_bridge.go:980` | `proxyOpenAIWSHTTPBridgeTurn` | `handleOpenAIWSFailureAccountSideEffects` |
| `openai_ws_http_bridge.go:1088` | `proxyOpenAIWSHTTPBridgeTurn` | `handleOpenAIWSTerminalTransientFailure` |
| `openai_ws_v2_passthrough_adapter.go:981` | `proxyResponsesWebSocketV2Passthrough` | `handleOpenAIWSDialTransientFailure` |
| `openai_ws_v2_passthrough_adapter.go:1596` | `proxyResponsesWebSocketV2Passthrough` | `handleOpenAIWSFailureAccountSideEffects` |
| `ratelimit_service.go:354` | `HandleUpstreamError` | `handleUpstreamErrorAfterStreakReset` |
| `ratelimit_service.go:360` | `handleUpstreamErrorAfterStreakReset` | `maybeDisableOpenAISchedulingOn429` |
