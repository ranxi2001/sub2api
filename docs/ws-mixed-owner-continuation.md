# Completed mixed-owner WebSocket continuation

With `openai_409_omit_encrypted_history_enabled` enabled, an existing client
WebSocket can rebuild a current, provably unsent turn containing opaque history
from multiple completed producers. This closes the live-socket gap between the
send-boundary owner check and the existing readable continuation handler.

The gateway requires server-observed, successfully delivered provenance for every
function/custom tool call used by WS history recovery. A matching client pair
alone is insufficient. A separate bounded record binds the authenticated user/key,
group/session/model, exact call type/ID/name/arguments, producing delivery, and
original owner/epoch/shadow associations. Pending, failed, conflicting, missing,
reissued or unrelated-task calls are refused. The client result is its completion
report, not proof that an external side effect executed.

Proof scope is deliberately model-specific: a call delivered under M1 cannot
be replayed under M2 merely because the socket later selected M2. The inherited
model regression earns its real call on M2 before omitting the model; a separate
negative regression retains the M1-to-M2 case and requires zero B dispatches.
Calls delivered before this change, expired/evicted proofs, and calls without a
completed owner/shadow anchor cannot use this automatic WS recovery. Normal
forwarding and the existing Beta-off behavior do not acquire a replay permit.
No database migration is required. Rolling back ignores the separate proof
namespace; it does not restore proofs that have expired.

The gateway also requires a completed ownership observation for **every** supplied
opaque item, readable current intent, and complete, unique tool call/result
pairs. It discards provider pointers and historical ciphertext. In this mixed
WS case, it carries each complete original call and corresponding result as
inert JSON text in an assistant message. Call ID, call type, name, arguments
or custom input, result type and extensions stay associated even when same-name
parallel tools return in reverse order. No top-level callable history remains.
Current tool definitions and
schemas remain available for new tool work. The client keeps its existing
session identity and socket; no client confirmation or new conversation is
required for this supported branch.

Call proofs are reserved before exposing provider output and committed only after
the successful downstream terminal and original owner/epoch publication. Recovery
and physical send recheck the same proof and associations. Records contain only
internal fingerprints and route/delivery metadata (128 per principal, maximum
32 KiB each, 30-minute TTL); eviction loses recoverability. They are not logged.

The permit comes from a private before-write route check. The public
`encrypted_agent_recovery_owner_conflict` code alone never permits retry.
Unknown ownership, pending/failed/conflicted delivery, missing/duplicate or
incomplete tool pairs, unsupported result representations, and changed shadow
associations remain refusals. All original completed routes are checked again
at the physical send, including after history omission. Historical owner
records are retained. Identity, authorization, moderation and recovered-body
privacy, billing, model policy, admission, payload integrity and no-replay
checks remain in the normal handler/forwarder path.

The request log event `openai.agent_recovery_refused` adds allowlisted internal
subreasons and owner/tool structural counts, plus shadow lookup/epoch states.
It never adds bodies, ciphertext, item/call hashes or identifiers, credentials,
route addresses, or account labels. `unknown` and partial/unexamined states are
explicit; these diagnostics do not confer permission. The public error code,
message, logical HTTP status and WebSocket error/close contract remain intact.

Regression evidence uses authenticated ingress, production forwarding, isolated
Redis and loopback fake providers. It includes turn 53, fresh/live sockets,
account binding changes, exactly-once client tool effects, final wire schemas,
same-name parallel calls with swapped arguments and out-of-order results,
and terminal negative controls. The synthetic provider computes its answer
from the actual received call/result association. The CI restores exact production blobs in a
disposable checkout for behavioral RED, then runs the candidate with the race
detector. Compile errors, skips and panics are not accepted as RED.

This source branch is not an attribution of the historical incident: its exact
internal refusal subreason was not recorded. The deployed baseline already
contains #855 and #860. A before-dispatch refusal proves only that the current
generation was unsent; it says nothing about earlier turns' tools or charges.
Real-provider acceptance is **NOT EXECUTED**. A passing local fake-provider
oracle is not a production deployment or a guarantee that a provider will
accept every reconstructed conversation.

Shadow snapshots omit original typed agent envelopes with no readable content
or summary. Retained agent metadata and readable text become assistant history
when the original non-agent input/instructions independently supplies intent.
A readable agent summary that supplies the only available intent keeps its
portable user representation. Genuine user text is never reclassified based on
a gateway-like prefix or marker. Ownership and epoch fences retain the original
envelopes, and the latest-user intent rules are unchanged.

Tool provenance uses the current resolved forward model. An active WebSocket
may omit the optional model field after selecting it; that omission does not
create a new proof scope. A different resolved model still cannot borrow the
old proof. HTTP projection refusals retain their fixed stage in Ops metadata,
with input/controls change booleans confined to the diagnostic journal.
