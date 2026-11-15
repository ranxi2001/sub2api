# Customer cost details evidence

Free Fast already charges the customer using a Standard calculation while
retaining the request's real service tier and Priority cost columns. The existing
shared cost popup now uses the saved Standard display snapshot: component amounts
and their sum are before the effective customer multiplier. The saved multiplier
explains `actual_cost`. No new label, badge, banner or explanatory UI copy is added.

`customer_cost_details` is optional, versioned request evidence. It is captured
only when the Standard calculation actually supplies the Free Fast charge, then
stored with the append-only usage row and durable billing event. It is never an
input to billing, pricing, ledger updates or account statistics. Migration 379 adds
a nullable JSONB column without backfilling old records. An older binary can
ignore the column; reverting the binary does not delete saved evidence.

For old complete token-only Priority records whose charge cannot be reconciled
with their stored components/rate, the customer component and price fields show
the existing unknown-value dash. Current group configuration never determines
historical pricing. Mixed image, per-request or additive-tool records without a
snapshot retain their existing recorded display; a differing total alone does
not prove Free Fast. Unknown/non-token/additive quotes are not invented. Normal
paid Priority, Standard and Flex displays retain their existing behavior.

The synthetic fixture is deliberately independent of customer data:

| Component | Recorded Priority | Customer Standard |
| --- | ---: | ---: |
| Input: 8,345 tokens | 0.16690000 | 0.08345000 |
| Output: 136 tokens | 0.01360000 | 0.00680000 |
| Cache creation: 1,197 tokens, 30m | 0.01496250 | 0.01496250 |
| Cache read: 37,469 tokens | 0.07493800 | 0.03746900 |
| Original total | 0.27040050 | 0.14268150 |

Standard total × 0.50 = 0.07134075 charged. Unit prices are 10/50 per million
input/output tokens; the request tier remains Fast. Cache creation demonstrates
why dividing every Priority component or total by two is incorrect.

Run `.github/scripts/free-fast-customer-cost-browser.mjs` from the repository root
with `PLAYWRIGHT_ROOT` pointing at an installed Playwright package or its parent,
and `FREE_FAST_SCREENSHOT_DIR` pointing outside the source tree. The harness uses
the complete source at baseline `0e8e97aa23afd8e88a61ea64a84a8fb84f1bfdc6`, then the
candidate shared Vue component, with real styles, locale messages and synthetic
HTTP/data fixtures. It captures 16 before/after views covering English/Chinese,
user/admin and desktop/mobile, after asserting the mathematical values, CSS and
viewport geometry. Its manifest includes fixture/harness/component/image hashes,
viewport sizes and actual DOM text.

Screenshot files belong in native GitHub PR attachments, never in this directory.
Download each uploaded attachment and compare its SHA256 with the manifest before
embedding it in the PR. The harness and CI workflow are reproducible evidence
runners, not a claim that their checks have already passed: consult the PR's
actual validation table and current-head CI results.


Snapshot version 2 adds disjoint cache-price evidence (`kind`, `tokens`, per-token
`unit_price`) from the same resolved Standard calculation. Reasoning/time/context
adjustments follow the component prices; the customer multiplier remains separate.
Version 1 keeps its known aggregates and shows unknown cache unit prices. No
migration/backfill is needed for the additive JSON field. Invalid or inconsistent
version 2 cache evidence is rejected rather than projected from current settings.
Restricted administrators receive the typed numeric snapshot through the explicit
allowlist, without nested user/key/account/group/subscription data.

The extended harness captures 32 new images: exact and mixed 30m/12h fixtures ×
before/after × English/Chinese × user/admin × desktop/mobile. Save them in a new
artifact directory; the original 16 images at capture commit 50f906972 and their
native attachments retain their original identity and hashes. The new matrix
asserts read $1, short write $12.5, long write $25 per million tokens alongside
component totals. Only the manifest's own head identifies a new capture.
