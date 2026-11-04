# VIP auto credit (Draft implementation)

Auto credit is an administrator-authorized advance recorded as a receivable,
not a payment. It must never create successful payment orders or paid revenue.
The fixed ledger note is `自动充值需索取费用`.

The first increment provides a PostgreSQL repository and migration 328, deliberately
leaving 327 available for open PR #644. No runtime trigger or UI is wired yet.
A disposable PostgreSQL test exercises save → issue → ledger → full settlement.

- Default disabled, explicit per-user opt-in. Saves never issue credit.
- All new amounts are decimal strings / NUMERIC(20,8), maximum 1,000,000 USD.
- Omitted fields preserve values; null clears monetary settings while disabled.
- Configuration updates require the current version; stale saves conflict.
- The user database row serializes config, credit, debit and full settlement.
- Outstanding is the sum of pending ledger entries. A fixed issue must entirely
  fit the remaining cap; partial credit and partial settlement are unsupported.
- A unique user/debit epoch prevents repeated issues without another real debit.
- Credit, ledger and audit commit atomically. Settlement only marks receivables
  collected; it never credits the balance again. Disabling retains history.

Required gates before readiness: atomic debit/staged-delivery wiring, bounded
balance-admission recovery, cache/region handling, admin-only routes, zh/en
responsive UI and notices, concurrency/rollback/upgrade tests, server/race/build
and frontend/browser checks. The PR remains Draft while these are open.

Reproduce the isolated repository test (never point at an existing service DB):

```bash
cd backend
VIP_CREDIT_TEST_DSN='host=127.0.0.1 port=55439 user=postgres dbname=vip_credit_test sslmode=disable' \
GOMAXPROCS=2 go test -p 1 -tags=vipcreditintegration ./internal/repository -run TestVipCredit -count=1 -v
```
