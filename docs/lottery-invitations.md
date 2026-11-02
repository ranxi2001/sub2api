# Invitation prize pools

Migration `325_lottery_invitations.sql` adds invitation rules, personal links and participation receipts. Existing pools, balances and tickets are retained. The site-wide lottery switch and new pools remain off by default.

## Rules

- `invite_visit`: the inviter qualifies on the first accepted browser visit. When both parties are rewarded, the invitee receives their tickets only after creating a new account.
- `invite_signup`: qualification requires a new account created after the recorded visit.
- `invite_recharge`: qualification requires registration followed by cumulative credited USD reaching the threshold. Online payments, balance codes and admin credits participate. Lottery awards, signup gifts and affiliate commissions do not.
- A receipt binds the first inviter per browser and pool. Registration must claim it within 30 days. A database constraint permits only one invitation per registered user and pool. Existing users and self-referrals do not qualify for signup rewards.
- Once participation exists, mode, threshold, recipients and ticket count are locked. Visit restrictions remain editable. Pausing grants preserves existing tickets.
- Refunds recalculate the recharge qualification. Already-used tickets become debt; previously issued prizes remain. Reaching the threshold again restores eligibility without granting an additional lifetime reward.

## Visit restrictions

The admin pool form provides a per-IP daily cap, a per-inviter daily cap, IPv4/IPv6 and CIDR exclusions, blocked two-letter country/region codes, and an unknown-country toggle. Limits are independent per pool, count accepted visits and reset at midnight UTC. They apply before recording a new invitation for any invitation rule. Country and IP policy changes do not retroactively remove accepted visits.

Client IP resolution uses `ip.GetTrustedClientIP`, including the existing verified-ingress path. Configure `server.trusted_proxies` for the actual reverse proxies. Browser-provided country values are never used. The existing server-side GeoJS resolver caches successful countries for 24 hours and bounds external lookup concurrency and duration. Private addresses, localhost and failed lookups are unknown. Unknown countries are denied by default; localhost testing requires explicitly disabling that option on the test pool.

The first-party visitor receipt is an opaque random HttpOnly, SameSite=Lax cookie, with Secure on HTTPS; only its SHA-256 digest is stored in participation records. Administrators can inspect accepted visits, IP, country, registration and award state. IP/country blocklists are omitted from user and public API responses. IP caps mitigate repeat visits; they do not establish that an anonymous visitor is a unique person.

## Transactions and recovery

Visit caps and participation creation hold the pool row lock, so different inviters share the same IP cap. All monetary paths use user, settings, then ascending pool locks. Recharge/refund qualification commits with the originating credit/refund. Recipient settlement follows commit, with award flags, tickets and ledger entries updated atomically per recipient. Durable qualification is reconciled again on pool reads and under the draw lock, preventing stale eligibility after a refund. Failed post-commit settlement is retried without duplicating rewards.

Email and OAuth registration receive the cookie through auth middleware. The registration bootstrap binds it; authenticated receipt claims recover interrupted registration callbacks. Normal registration remains available when invitation rewards are denied.

## Verification

Backend tests: `go test ./internal/service ./internal/handler -run TestLottery`; PostgreSQL tests: `LOTTERY_TEST_DSN=<isolated database containing sub2api_lottery_test> go test -tags=integration ./internal/service -run TestLottery -count=1`.

Frontend component tests cover invitation form serialization, locked rewards with editable restrictions, animation continuity, existing draw recovery and responsive result behavior. Browser checks use intercepted fixture draws to preserve preview balances and tickets.
