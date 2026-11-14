package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// 「降智开 BPS」规则的账号处置。与摘分组/停调度共用租约与账号行锁（调用方已持有），
// 区别在于：失败要累计连续次数、还可按用量触发；动作是改写账号 Extra 里的 BPS 开关与选项，
// 恢复时按开启前的快照逐键还原。
func applyQualityBPSOutcome(ctx context.Context, tx *sql.Tx, plan *service.ScheduledTestPlan, outcome, status string, state qualityState, hasState bool) (string, error) {
	var policy *service.QualityBPSPolicy
	if q := plan.PelicanConfig.Quality; q != nil {
		policy = q.BPS
	}
	account, extra, err := qualityBPSAccount(ctx, tx, plan.AccountID)
	if err != nil {
		return "", err
	}
	usage, hasUsage := service.QualityUsagePercent(account.Extra, time.Now())

	if state.Action == service.QualityActionEnableBPS {
		enabled, _ := account.Extra["openai_excel_bps"].(bool)
		autoRestore := plan.PelicanConfig.Quality.AutoRestore
		// 连续满血轮数：降智清零，无法判断不打断也不累加；到关闭次数后不再往上加。
		threshold := service.QualityBPSPassThreshold(policy)
		passStreak := state.PassStreak
		switch {
		case outcome == "failed":
			state.PassStreak = 0
		case outcome == "passed" && autoRestore && state.PassStreak < threshold:
			state.PassStreak++
		}
		if state.PassStreak != passStreak {
			if err := qualityUpsertState(ctx, tx, plan.ID, state); err != nil {
				return "", err
			}
		}
		switch {
		case outcome == "failed" && !enabled && account.Extra[service.ExcelBPS403DisabledAtKey] != nil:
			return "bps_blocked_403", nil
		case outcome == "failed":
			return "already_quarantined", nil
		case outcome == "inconclusive":
			return "inconclusive", nil
		case !autoRestore:
			return "passed", nil
		case state.PassStreak < threshold:
			return fmt.Sprintf("restore_counted:%d/%d", state.PassStreak, threshold), nil
		case service.QualityBPSHoldForUsage(policy, usage, hasUsage):
			return "bps_kept_usage", nil
		}
		// 只还原本规则写入的值：账号被人改过 BPS（或 403 自动关了）就交给管理员处理。
		if status != "active" {
			plan.QualityRestoreDiagnostic = qualityRestoreConflict("account_not_active")
			return "restore_conflict", nil
		}
		current := qualityBPSSnapshot(extra)
		if !qualityBPSSnapshotEqual(current, state.BPSApplied) {
			diagnostic := qualityRestoreConflict("bps_fields_changed")
			for _, key := range service.QualityBPSManagedKeys {
				if current[key] == nil && state.BPSApplied[key] == nil {
					continue
				}
				if !qualityBPSSnapshotEqual(map[string]json.RawMessage{key: current[key]}, map[string]json.RawMessage{key: state.BPSApplied[key]}) {
					diagnostic.ChangedFields = append(diagnostic.ChangedFields, key)
				}
			}
			plan.QualityRestoreDiagnostic = diagnostic
			return "restore_conflict", nil
		}
		set := make(map[string]json.RawMessage, len(state.BPSPrevious))
		var remove []string
		for _, key := range service.QualityBPSManagedKeys {
			if value, ok := state.BPSPrevious[key]; ok {
				set[key] = value
			} else {
				remove = append(remove, key)
			}
		}
		// Cancelling the rule while login runs must cancel automatic activation.
		remove = append(remove, service.ExcelBPSAuthorizationPendingKey)
		if err := qualityBPSPatchExtra(ctx, tx, plan.AccountID, set, remove); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM account_quality_states WHERE plan_id=$1`, plan.ID); err != nil {
			return "", err
		}
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &plan.AccountID, nil, nil); err != nil {
			return "", err
		}
		return "restored", nil
	}

	// 尚未开启：先记连续降智次数（无法判断不打断也不累加，通过清零），再看是否触发。
	switch outcome {
	case "failed":
		state.FailureStreak++
	case "passed":
		state.FailureStreak = 0
	}
	trigger := service.QualityBPSTrigger(policy, state.FailureStreak, usage, hasUsage)
	action := ""
	switch {
	case trigger == "":
	case !service.QualityBPSEligible(account):
		action = "bps_unsupported"
	case account.Extra[service.ExcelBPS403DisabledAtKey] != nil:
		// BPS 曾因 403 被自动关闭（疑似 Excel 封禁），由管理员或独立的每小时恢复探测重新开启前，本规则不再自动打开。
		action = "bps_blocked_403"
	case account.Extra["openai_excel_bps"] == true:
		// 已由管理员开着 BPS：不接管、不覆盖他的选项，也不会在恢复时替他关掉。
		action = "bps_already_enabled"
	}
	if trigger == "" || action != "" {
		if err := qualityBPSSaveStreak(ctx, tx, plan.ID, state, hasState); err != nil {
			return "", err
		}
		switch {
		case action != "":
			return action, nil
		case outcome == "failed" && policy != nil && policy.FailureThreshold > 0:
			return fmt.Sprintf("failure_counted:%d/%d", state.FailureStreak, policy.FailureThreshold), nil
		case outcome == "failed":
			return "failure_counted", nil
		case outcome == "passed" || outcome == "inconclusive":
			return outcome, nil
		}
		return "no_change", nil
	}

	pending := false
	if !account.IsExcelOAuth() {
		var ready bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM openai_excel_oauth_credentials WHERE account_id=$1 AND credentials_ciphertext<>'')`, plan.AccountID).Scan(&ready); err != nil {
			return "", err
		}
		pending = !ready
	}
	set := map[string]json.RawMessage{}
	var remove []string
	if pending {
		set[service.ExcelBPSAuthorizationPendingKey] = json.RawMessage("true")
	} else {
		remove = append(remove, service.ExcelBPSAuthorizationPendingKey)
	}
	for key, value := range service.QualityBPSExtra(policy) {
		if value == nil {
			remove = append(remove, key)
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		set[key] = raw
	}
	// 开启即确认之前的 403 自动关闭记录，与账号编辑页开启 BPS 的行为一致。
	remove = append(remove, service.ExcelBPS403DisabledAtKey)
	if err := qualityBPSPatchExtra(ctx, tx, plan.AccountID, set, remove); err != nil {
		return "", err
	}
	_, applied, err := qualityBPSAccount(ctx, tx, plan.AccountID)
	if err != nil {
		return "", err
	}
	state = qualityState{
		Action:        service.QualityActionEnableBPS,
		FailureStreak: state.FailureStreak,
		BPSPrevious:   qualityBPSSnapshot(extra),
		BPSApplied:    qualityBPSSnapshot(applied),
	}
	if err := tx.QueryRowContext(ctx, `SELECT updated_at FROM accounts WHERE id=$1`, plan.AccountID).Scan(&state.AccountVersion); err != nil {
		return "", err
	}
	if err := qualityUpsertState(ctx, tx, plan.ID, state); err != nil {
		return "", err
	}
	if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &plan.AccountID, nil, nil); err != nil {
		return "", err
	}
	if pending {
		return "bps_authorizing", nil
	}
	if trigger == "usage" {
		return "bps_enabled_usage", nil
	}
	return "bps_enabled", nil
}

// qualityBPSAccount 读取判断 BPS 资格所需的最小账号信息；Extra 同时以原始 JSON 返回，
// 用于逐键快照（保留数字等原样，不经 float64 转换）。
// ********  个人感觉不太实用，以后再改 AI别动此处，也别改注释  ************
func qualityBPSAccount(ctx context.Context, tx *sql.Tx, accountID int64) (*service.Account, map[string]json.RawMessage, error) {
	account := &service.Account{ID: accountID}
	var parentID sql.NullInt64
	var credentials, extra []byte
	err := tx.QueryRowContext(ctx, `SELECT platform, type, parent_account_id, COALESCE(credentials,'{}'::jsonb), COALESCE(extra,'{}'::jsonb)
 FROM accounts WHERE id=$1`, accountID).Scan(&account.Platform, &account.Type, &parentID, &credentials, &extra)
	if err != nil {
		return nil, nil, err
	}
	if parentID.Valid {
		account.ParentAccountID = &parentID.Int64
	}
	if err := json.Unmarshal(credentials, &account.Credentials); err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(extra, &account.Extra); err != nil {
		return nil, nil, err
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(extra, &raw); err != nil {
		return nil, nil, err
	}
	return account, raw, nil
}

func qualityBPSSnapshot(extra map[string]json.RawMessage) map[string]json.RawMessage {
	snapshot := map[string]json.RawMessage{}
	for _, key := range service.QualityBPSManagedKeys {
		if value, ok := extra[key]; ok {
			snapshot[key] = value
		}
	}
	return snapshot
}

// 按设置语义比较：账号编辑页会省略 false 开关和默认代理来源。
// 模型范围等没有等价缺省值的字段仍严格比较，避免覆盖真正的手动改动。
func qualityBPSSnapshotEqual(a, b map[string]json.RawMessage) bool {
	keys := make(map[string]struct{}, len(a)+len(b))
	for key := range a {
		keys[key] = struct{}{}
	}
	for key := range b {
		keys[key] = struct{}{}
	}
	for key := range keys {
		left, right := a[key], b[key]
		if left == nil {
			left = qualityBPSDefaultJSON(key)
		}
		if right == nil {
			right = qualityBPSDefaultJSON(key)
		}
		var x, y any
		if json.Unmarshal(left, &x) != nil || json.Unmarshal(right, &y) != nil || !reflect.DeepEqual(x, y) {
			return false
		}
	}
	return true
}

func qualityBPSDefaultJSON(key string) json.RawMessage {
	switch key {
	case service.ExcelBPS403RecoveryIntervalMinutesKey:
		return json.RawMessage(`60`)
	case "openai_excel_bps", service.ExcelBPSOmitUnsupportedToolsKey,
		service.ExcelBPSIgnoreEncryptedContentKey, service.ExcelBPSWebSearchKey, service.ExcelBPSWebSearchAutoKey,
		"openai_excel_bps_auto_disable_on_403", service.ExcelBPSAutoRecoverOn403Key, service.ExcelBPSAutoMoveOn403Key,
		"openai_excel_bps_mihomo", "openai_excel_bps_cache_creation_as_input":
		return json.RawMessage(`false`)
	case service.ExcelBPSProxySourceKey:
		return json.RawMessage(`"mihomo"`)
	default:
		return nil
	}
}

func qualityBPSPatchExtra(ctx context.Context, tx *sql.Tx, accountID int64, set map[string]json.RawMessage, remove []string) error {
	payload, err := json.Marshal(set)
	if err != nil {
		return err
	}
	if remove == nil {
		remove = []string{}
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra=(COALESCE(extra,'{}'::jsonb) || $2::jsonb) - $3::text[], updated_at=clock_timestamp() WHERE id=$1`,
		accountID, string(payload), pq.Array(remove))
	return err
}

// 连续次数为 0 且没有其他记录时不留状态行，避免每个健康账号都占一行。
func qualityBPSSaveStreak(ctx context.Context, tx *sql.Tx, planID int64, state qualityState, hasState bool) error {
	if state.FailureStreak == 0 && state.Action == "" {
		if !hasState {
			return nil
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM account_quality_states WHERE plan_id=$1`, planID)
		return err
	}
	return qualityUpsertState(ctx, tx, planID, state)
}

func qualityUpsertState(ctx context.Context, tx *sql.Tx, planID int64, state qualityState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_quality_states(plan_id,state) VALUES($1,$2)
 ON CONFLICT (plan_id) DO UPDATE SET state=EXCLUDED.state`, planID, string(data))
	return err
}
