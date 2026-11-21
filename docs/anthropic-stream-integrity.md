# Anthropic stream integrity (Issue 985)

This change covers Anthropic upstream streams consumed by Chat Completions,
Responses and native adapters, including buffered clients. Synthetic fixtures
are not a replay of historical requests: no historical upstream frames exist.

Acceptance contract:

- Assemble bounded SSE frames at blank lines, including comments, metadata,
  multiple data lines, CRLF, compact colons, reordered fields, network splits
  and a final unterminated frame. Named events must agree with JSON types;
  unknown events remain forward compatible. Data-only SSE is a compatibility
  input, not a claim about the supplier's native protocol contract.
- Require a provider terminal, semantic output (text, tools or reasoning), and
  observed input/output usage fields. Missing usage is incomplete, distinct
  from explicit zero. Errors and client cancellation cannot become success.
- Preserve model identity, tool schemas, call IDs, continuation, all known
  token/cache buckets and partial costs. Never estimate historical charges.
- After output, emit one protocol failure without a successful terminal or
  HTTP status rewrite. Incomplete usage must not trigger account replay.
- Diagnostics contain only counters, presence flags and status. No production
  capture, payload logging, UI fields, schema migration or historical rewrite.

Required remote evidence (pending in the initial Draft): identical regression
test blobs against fixed historical readers at
`dfcc66238b28c8a0392c24d418d81120f95f682f` (business RED) and a clean candidate
(GREEN); real handler/loopback upstream/client accumulation and tool follow-up;
isolated PostgreSQL billing event, quote and persistence readback; boundary,
cancellation, lifecycle and race tests; existing full unit/integration CI and
ordinary server build. Evidence validation rejects missing selected tests,
skips, build failures, panic/crash and race contamination, with negative controls.

The fixed-baseline RED/GREEN workflow is preserved at
`.github/workflow-archive/anthropic-stream-integrity-red-green.yml` after its
remote evidence passed. The active path-triggered workflow retains reusable
GREEN/race/SDK/PostgreSQL/build tests and strict evidence validation. Draft remains Draft until independent source review and all gates pass.
