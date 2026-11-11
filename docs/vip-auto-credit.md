# VIP auto credit

Auto credit is an administrator-authorized advance recorded as a receivable,
not a channel payment. It never creates paid orders or changes total_recharged.
The administrator's user balance history also includes every issued advance,
including historical and settled entries, under the `auto_credit` type filter.
Each entry shows its amount, original issue time, ledger ID, balances and
collection status. Displayed amounts and balances use two decimal places across
the user list, credit dialog, collection confirmation and balance history.
Ledger values and editable configuration retain their original precision. Collection updates that entry rather than adding another
recharge. These advances remain excluded from `total_recharged`; no backfill or
balance mutation is needed to display old records.
The fixed ledger note is `自动充值需索取费用`.

Administrators open **VIP auto credit** on a user row. Pending debt is also shown
beside the user's balance and opens the same dialog. The dialog supports zh/en,
small screens, exact decimal configuration, filtered/paginated history and a
second confirmation for recording full collection. Users and delegated admin
keys cannot configure credit or settle another user's receivables.

- Default disabled; threshold, amount and cap must all be strictly positive,
  no more than 1,000,000 USD and at most 8 decimal places.
- Saves never issue credit. The next real balance debit evaluates one fixed
  issue. A request rejected for insufficient balance may recover once before
  fresh authentication, including the initial zero-balance epoch.
- New amounts use decimal strings and SQL NUMERIC(20,8). Omitted fields preserve
  values; null clears monetary settings while disabled. Saves require a version.
- User row locks serialize config, debit, issue and full settlement across
  processes. A unique user/debit epoch prevents repeated credit without a new
  real debit; existing usage identity dedup also covers staged recovery.
- Outstanding is SUM(pending ledger entries). The full fixed issue must fit the
  remaining cap. Partial credit and partial settlement are unsupported.
- Balance addition, ledger, audit and cache receipts commit atomically. Full
  collection only settles debt and audits the administrator; it never adds
  balance again. Disabling or clearing config retains all history.
- History has 50-entry cursor pages with has_more/next_before_id. The debt sum
  includes every pending record, independently of pagination and status filter.
- Credit users read authoritative DB balance for billing eligibility/reserves,
  including disabled configurations. This prevents arbitrarily late legacy
  Redis publications from restoring old balances. Durable two-pass invalidation
  retries Redis failures without repeating the monetary operation.
- Multi-group selection preserves subscription fallback when neither current
  balance nor one eligible credit reaches the configured minimum reserve.
  Single-group authentication keeps its historical <=0 hard gate.
- Regional replicas never issue credit or write primary balances. A stale or
  unavailable regional snapshot fails closed until normal synchronization;
  this does not add a regional monetary-authority RPC.

Migrations 328 (ledger/config/audit), 329 (balance invalidation receipts) and 330
(strictly positive threshold) are forward-only. Migration 330 disables/clears
legacy zero thresholds while preserving existing ledger, balance and audit.
Rolling back a binary does not remove receivables or reverse credited balances.

Real isolated PostgreSQL + Redis regressions (never use existing service data):

```bash
cd backend
VIP_CREDIT_TEST_DSN='host=127.0.0.1 port=55439 user=postgres dbname=vip_credit_test sslmode=disable' \
VIP_CREDIT_TEST_REDIS_ADDR='127.0.0.1:55440' \
GOMAXPROCS=2 go test -p 1 -tags=vipcreditintegration ./internal/repository -run TestVipCredit -count=1 -v
```

CI runs the custom tag with PostgreSQL16 and Redis7 services, explicit required
connection settings and race enabled. Missing settings fail tests rather than
silently skipping them. Backend mocks are separate from these real repositories.

The isolated browser entry is `frontend/src/__fixtures__/vip-auto-credit.html`.
The recorded screenshot harness uses the actual component/project CSS, a
fixture-only administrator and intercepted API; screenshots are local artifacts,
not production evidence. Full production build/race/current-head CI remain
explicit gates in the Draft PR until independently completed.
