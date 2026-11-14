# BPS account web search

Accounts explicitly enable `extra.openai_excel_bps_web_search=true`. Existing
accounts keep the prior native fallback/hosted-tool omission policy. The separate
`extra.openai_excel_bps_web_search_auto=true` option supplies search when a client
omits it and allows automatic tool selection. It requires web search enabled and
never overrides `none`, `required`, or a named client tool. Controlled experiments
never add search submissions.

The gateway sends authenticated search requests to
`https://chatgpt.com/backend-api/codex/alpha/search`, using the selected BPS grant,
account ID and pinned proxy. It does not forward client cookies or Codex tickets,
follow redirects, provision another account, or require a Claude router plugin.
The endpoint is not a public, stable OpenAI API; entitlement and wire changes can
cause rejection. This implementation has offline coverage, not production search
or billing acceptance.

Explicit `web_search` and preview declarations support automatic/forced text
search. `search_context_size` maps low/medium/high to short/medium/long response
length hints. Mixed text/image search declarations provide text results only;
image-only search, location constraints, unsupported fields and nonempty filters
are rejected. Domain filters are not claimed: the alpha provider has no verified
filter contract, and filtering displayed citations would not constrain retrieval.

`external_web_access=false` never enables live retrieval. Optional cache-only
search is removed with an explicit limitation notice while other tools/chat
continue; forced cache-only search fails before generation. Search history replays
only the supplied query/source metadata and labels missing result text as absent.

A private request-local tool drives the search loop. It is attached after catalog
cache publication, is not inherited by another request, and is never dispatched
to clients. Each entire tool batch and its original schema must validate before
search. Mixed client/search batches and parallel searches are rejected; a repeated
call/item ID fails without repeating the operation. Search results are untrusted
data in a same-account native continuation. Search failures do not replay, switch
accounts, move groups, disable BPS, or fabricate search results.

Ordinary text and reasoning summaries stream before terminal completion. The
bridge preserves output indexes, first response ID, content and summary identity,
and validates streamed text against terminal content. Forced-search prose waits
until search succeeds; structured final answers retain the existing JSON/schema
validation, and intermediate search prose is withheld. Hosted `web_search_call`
progress and sources are visible; citations are attached only to literal source
URLs actually present in final text. No fixed four-search limit is imposed.

The loop has a 10-minute context deadline, 90 seconds per search including its
slot wait, 16 concurrent search HTTP operations per process, 2 MiB per search
result and 32 MiB continuation/aggregate output limits. The gateway sends SSE keepalives while search waits. Client cancellation closes active response bodies and cancels searches
and continuation requests. These bounds are not an unlimited-runtime promise.

All completed BPS generation rounds contribute to final model token usage, also
on search failure after generation. The standalone search endpoint does not
provide an established billable token contract here; no synthetic token charge
is invented for it. Any provider-side search cost/entitlement needs separate
operational verification before enabling accounts. No real paid call is part of
CI or local validation.

## Management entry points

The account editor, bulk editor, OAuth BPS defaults card and quality-rule BPS
settings share the same search controls. Automatic search is disabled and cleared
when its parent search switch is off. Quality-rule snapshots manage both fields.
The notice ID `bps-account-search-initial-announcement` is shared across these
entry points and dismissed per administrator/browser using existing localStorage.
