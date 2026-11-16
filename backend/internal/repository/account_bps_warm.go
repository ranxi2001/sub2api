package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.BPSWarmAccountRepository = (*accountRepository)(nil)

// Only fields read by excelBPSWarmTargets and its eligibility helpers cross the
// database connection. Do not select the full credentials/extra JSON or hydrate
// relations here: this query runs on every request replica every five seconds.
const bpsWarmAccountsSQL = `
SELECT id, platform, type, status, schedulable, concurrency, parent_account_id,
       jsonb_build_object(
           'plan_type', credentials->'plan_type',
           'auth_mode', credentials->'auth_mode',
           'openai_auth_mode', credentials->'openai_auth_mode'
       ),
       jsonb_build_object(
           'openai_excel_bps', extra->'openai_excel_bps',
           'openai_excel_bps_mihomo', extra->'openai_excel_bps_mihomo',
           'openai_excel_bps_proxy_source', extra->'openai_excel_bps_proxy_source',
           'openai_excel_bps_proxy_mode', extra->'openai_excel_bps_proxy_mode',
           'openai_excel_bps_proxy_ids', extra->'openai_excel_bps_proxy_ids',
           'openai_excel_bps_authorization_pending', extra->'openai_excel_bps_authorization_pending',
           'openai_excel_bps_auto_recover_on_403', extra->'openai_excel_bps_auto_recover_on_403',
           'openai_excel_bps_auto_disable_on_403', extra->'openai_excel_bps_auto_disable_on_403',
           'openai_excel_bps_403_disabled_at', extra->'openai_excel_bps_403_disabled_at',
           'openai_siwc', extra->'openai_siwc'
       ) || CASE WHEN extra ? 'openai_proxy_mode'
                 THEN jsonb_build_object('openai_proxy_mode', extra->'openai_proxy_mode', 'openai_proxy_ids', extra->'openai_proxy_ids')
                 ELSE '{}'::jsonb END
FROM accounts
WHERE deleted_at IS NULL AND platform = $1 AND status = $2
ORDER BY priority, id`

func (r *accountRepository) ListBPSWarmAccounts(ctx context.Context) ([]service.Account, error) {
	rows, err := r.sql.QueryContext(ctx, bpsWarmAccountsSQL, service.PlatformOpenAI, service.StatusActive)
	if err != nil {
		return nil, fmt.Errorf("list BPS warm account hints: %w", err)
	}
	defer func() { _ = rows.Close() }()

	accounts := make([]service.Account, 0)
	for rows.Next() {
		var account service.Account
		var parent sql.NullInt64
		var credentials, extra []byte
		if err := rows.Scan(&account.ID, &account.Platform, &account.Type, &account.Status,
			&account.Schedulable, &account.Concurrency, &parent, &credentials, &extra); err != nil {
			return nil, fmt.Errorf("scan BPS warm account hints: %w", err)
		}
		if parent.Valid {
			account.ParentAccountID = &parent.Int64
		}
		if err := json.Unmarshal(credentials, &account.Credentials); err != nil {
			return nil, fmt.Errorf("decode BPS warm eligibility: %w", err)
		}
		if err := json.Unmarshal(extra, &account.Extra); err != nil {
			return nil, fmt.Errorf("decode BPS warm flags: %w", err)
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read BPS warm account hints: %w", err)
	}
	return accounts, nil
}
