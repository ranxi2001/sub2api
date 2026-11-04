# WS semantic first-token timing

User-authorized semantic timing: a typed reasoning output_item/part immediately establishes first token, without waiting for body text. This is distinct from visible-content timing.

Scope: ctx_pool ingress, WS-to-SSE forwarder, passthrough relay, and WS HTTP bridge. Sources: user report and local source inspection at `7250774b684095ea201c9bd04a7e3555006544ce`; synthetic fixtures only.

| Metric | Existing start (unchanged) | End / source | Missing |
| --- | --- | --- | --- |
| ctx_pool `FirstTokenMs` | `sendAndRelay`: after admission, before RPM/send | first typed reasoning item/part, or nonempty text/reasoning/tool/media content; request observation clock | nil if no qualifying event |
| WS-to-SSE `FirstTokenMs` | forwarder supplied request `startTime` | same semantic event at WS read | nil if no qualifying event |
| passthrough per-turn `FirstTokenMs` | accepted frame / existing `FirstTurnStartedAt` and `TakeNextTurnStartedAt` | same semantic event at relay read; `Now` | nil if no qualifying event; never inherit session first token |
| HTTP bridge `FirstTokenMs` | bridge turn entry, before admission / HTTP send | same semantic event at parsed SSE data line; request clock | nil if no qualifying event |
| SSE legacy semantic `first_token_ms` | supplied HTTP forward start | historical first non-preamble event, including some empty/untyped items and terminal events | existing rules retained |
| SSE visible mode | supplied HTTP forward start | actual nonempty visible text/tool/image | existing rules retained |
| first upstream event | same path-specific start | first recognized event, including created/metadata | nil if absent |
| first actual content | same path-specific start | `openai.ResponseContentKind`; excludes empty typed reasoning structures | nil if no content |
| duration | existing path-specific start above | existing completion/cleanup boundary | existing error-result rules |
| queue timing | existing arrival and forward boundaries | existing collector | unknown stays unknown |

A typed `item.type=reasoning` on output_item.added/done or typed reasoning summary/text part establishes observable semantic activity even with an empty summary. An arbitrary output item does not. Nonempty string reasoning/text/tool deltas and body snapshots qualify; empty/null/object deltas, created/in_progress, metadata, heartbeat, and terminal-only usage do not. Whitespace in a real text delta is content. Terminal response snapshots alone do not invent streamed TTFT. These observations do not establish provider compute time or browser end-to-end latency.

SSE historical semantic classification is broader than this contract. It is deliberately isolated: this patch aligns WS with meaningful reasoning/content, without rewriting SSE, first-output guards, retry eligibility, identity, billing or scheduling. No-output never authorizes replay. Each turn retains only its first qualifying observation; abnormal EOF/cancel retains existing result availability rules.

Chain: WS/bridge `OpenAIForwardResult.FirstTokenMs`, passthrough `RelayTurnResult.FirstTokenMs` → adapter / handler `AfterTurn` → `openai_gateway_usage.go` assigns `UsageLog.FirstTokenMs` → repository `nullInt` writes nullable `first_token_ms`, query reconstructs pointer only for valid SQL value → DTO `first_token_ms` → user/admin usage tables, account performance and ops aggregates. No schema or historical rewrite. Historical rows retain the old endpoint meaning; mixed histories are not comparable as a uniform latency series. Missing stays NULL, distinct from measured zero.

TPS consumers (`frontend/src/utils/usageTps.ts`, account performance and ops aggregates) divide output tokens by total recorded duration, without subtracting first-token latency. This endpoint correction does not change that denominator or TPS mathematically; latency bars and TTFT aggregates can change for newly recorded rows. Separate queue-inclusive first-token projections inherit the corrected endpoint, with existing starts/queue formulas intact.

Validation evidence and remaining acceptance are recorded in the Draft PR and `/data/development/artifacts/ws-semantic-first-token/`. No real supplier latency improvement is claimed.

Source references (line numbers at this revision):

- Persistence assignment: `backend/internal/service/openai_gateway_usage.go:492`; nullable insert: `backend/internal/repository/usage_log_repo_insert.go:1286`; SQL read: `backend/internal/repository/usage_log_repo_query.go:642` (only Valid reconstructs pointer), `:734` (nil maps to SQL NULL).
- Public DTO: `backend/internal/handler/dto/types.go:663`; mapper: `backend/internal/handler/dto/mappers.go:748`; frontend type: `frontend/src/types/index.ts:2023`.
- Actual usage-table selection: `frontend/src/components/admin/usage/UsageTable.vue:801` (measured server timing or first_token_ms); user export: `frontend/src/views/user/UsageView.vue:702`; TPS: `frontend/src/utils/usageTps.ts:19`.
- Passthrough per-turn adapter projection: `backend/internal/service/openai_ws_v2_passthrough_adapter.go:1482`; partial-result projection: `:1618`. Service/handler can still return unavailable result on some abnormal paths; this patch does not change failure settlement or create results.

Evidence limits: socket regressions run the real production forwarder and passthrough adapter with local WS on both hops, but do not exercise authenticated handler settlement into a database. SQL nullable write/read and UI field wiring above are source inspection, not a new database/UI end-to-end acceptance claim. Relay interrupted-stream regression explicitly tests timing snapshot availability; existing EOF no-replay and cancel-cleanup tests do not alone assert every handler timing field. Full database/UI acceptance remains pending.
