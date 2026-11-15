# Completed mixed-owner WebSocket continuation

With `openai_409_omit_encrypted_history_enabled` enabled, an existing client
WebSocket can rebuild a current, provably unsent turn containing opaque history
from multiple completed producers. This closes the live-socket gap between the
send-boundary owner check and the existing readable continuation handler.

The gateway requires a completed ownership observation for **every** supplied
opaque item, readable current intent, and complete, unique tool call/result
pairs. It discards provider pointers and historical ciphertext. In this mixed
WS case, it carries completed tool names/results as inert assistant text and
removes historical callable records/arguments. Current tool definitions and
schemas remain available for new tool work. The client keeps its existing
session identity and socket; no client confirmation or new conversation is
required for this supported branch.

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
and terminal negative controls. The CI restores exact production blobs in a
disposable checkout for behavioral RED, then runs the candidate with the race
detector. Compile errors, skips and panics are not accepted as RED.

This source branch is not an attribution of the historical incident: its exact
internal refusal subreason was not recorded. The deployed baseline already
contains #855 and #860. A before-dispatch refusal proves only that the current
generation was unsent; it says nothing about earlier turns' tools or charges.
Real-provider acceptance is **NOT EXECUTED**. A passing local fake-provider
oracle is not a production deployment or a guarantee that a provider will
accept every reconstructed conversation.
