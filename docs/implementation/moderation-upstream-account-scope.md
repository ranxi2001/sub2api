# Frozen semantics and execution plan

Base: 4189ed821 (origin/production). Sole application writer; all runtime validation and screenshots in GitHub Actions; no production, paid probes, merge or deployment.

Existing group scope is explicit: all_groups=true includes every group (including no group); all_groups=false uses group_ids, and an empty list matches nothing. Preserve this exactly. New upstream_account_scope defaults to all for missing legacy data; selected intersects the original request group/model filters with stable current-site upstream account IDs. selected with an empty list matches nothing, never everything. All groups plus selected accounts is the explicit account-only mode. Reject unknown scope on save and fail closed on persisted unknown values.

The selected IDs denote provider accounts, not the caller/user/API key. Validate IDs, supported real platform/type and existence server-side. Persist account binding metadata (platform, type and group membership at selection); revalidate before selected generation, reject stale/deleted/moved bindings with an observable error instead of silently broadening policy. Account group movement must not broaden coverage. No SQL schema migration: additive settings JSON; missing fields preserve legacy behavior. Before rolling back to old binaries, reset scope to all (old binaries ignore account restrictions and would audit more traffic).

Trace: handler checkSecurityAudit currently executes before scheduler selection; coordinator adapts legacy ContentModerationService.Check and independently executes prompt audit/penalty admission. HTTP completion cache currently marks the whole request. OpenAI ResponsesWebSocket audits first_turn before selection; per-turn BeforeRequestWithClientPayload sees the selected connection account and is shared by native/passthrough paths. A request may retry/fail over through multiple selected accounts.

Keep existing pre-route admission and prompt audit semantics. Defer ONLY account-scoped content moderation until an actual account is selected, before generation. Retain a frozen original audit identity/content for HTTP failover and the original client frame for each WS turn. Scope-check each selected attempt before reusing an allowed audit; an unselected attempt must not mark moderation complete. Cache completed selected audit only for the same logical content/turn; next WS turn starts fresh. Preserve permission, group, queue and billing revalidation and release paths.

Acceptance starts with a real WS handler/scheduler + loopback audit/provider fixture: a non-selected account must produce generation with zero audit calls. Baseline currently audits and blocks; RED must be a business assertion. Extend with selected deny (zero generation writes), HTTP types/stream modes, failover, WS next turn/native/pool/bridge, persistence/validation, selector integration and A→B→C badge behavior. Capture rendered original UI remotely before changing it. No external provider/network fixture requests.

UI: reuse QualityAccountSelector with fixed manual selection, server search and pagination, stable selected-ID union across hidden pages, cancellable/fenced asynchronous reads and batch selection. Account filters describe discovery only; saved IDs control policy. Chinese and English explain group AND account semantics and empty selection. Add registered NewFeatureBadge.


## Review repairs and explicit operational semantics

The admin HTTP DTO passes scope and IDs to service validation; stored binding metadata remains server-owned. A retained ID whose platform/type/group membership changed cannot be silently rebound by an unrelated save or by resubmitting the same IDs. Remove it and save, then select it again to explicitly approve a new binding. Switching to all clears the selection in the UI.

Post-selection policy errors use a typed local denial. HTTP callers return after their existing slot/queue release, and the scheduler also ignores that error defensively. WebSocket denials carry the existing local-admission sentinel. JSON image keepalive responses finish with one JSON document; SSE stays framed as an error event. Async workers rebind the immutable state to their own response context.

Audit turn identity is connection-wide, independent of transport counters that restart during same-account retry or failover. A replay retains the prior decision; a newly received client turn advances the audit identity even when its body is identical. All legacy audit entry points were traced, including protocol adapters, media, batch submit, Live creation, cached-content creation, embeddings and alpha/standalone search. Alpha commands and batch-item prompts are derived from the frozen original request for selected-account moderation; existing pre-route prompt auditing is unchanged.

Remote fixtures distinguish configuration persistence, actual generation writes, and rendered UI. Neither an offline browser adapter nor a configuration-only test proves routing acceptance. The acceptance workflow requires named tests and rejects compilation/setup errors, panic, race, skipped/missing tests and crashes.
