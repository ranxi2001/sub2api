package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type qualityRuleTemplateRepository struct {
	client *dbent.Client
	db     *sql.DB
}

func NewQualityRuleTemplateRepository(client *dbent.Client, db *sql.DB) service.QualityRuleTemplateRepository {
	return &qualityRuleTemplateRepository{client: client, db: db}
}

// plan_ids lists the rules the template created that still exist on live accounts.
const qualityRuleTemplateColumns = `
	t.id, t.account_filter, t.model_id, t.cron_expression, t.enabled, t.max_results, t.pelican_config,
	t.last_synced_at, t.created_at, t.updated_at,
	ARRAY(
		SELECT l.plan_id FROM quality_rule_template_accounts l
		JOIN scheduled_test_plans p ON p.id = l.plan_id
		JOIN accounts a ON a.id = p.account_id AND a.deleted_at IS NULL
		WHERE l.template_id = t.id
		ORDER BY l.plan_id
	)`

func scanQualityRuleTemplate(row scannable) (*service.QualityRuleTemplate, error) {
	t := &service.QualityRuleTemplate{}
	var filter, config []byte
	var planIDs pq.Int64Array
	if err := row.Scan(&t.ID, &filter, &t.ModelID, &t.CronExpression, &t.Enabled, &t.MaxResults, &config,
		&t.LastSyncedAt, &t.CreatedAt, &t.UpdatedAt, &planIDs); err != nil {
		return nil, err
	}
	if len(filter) > 0 {
		if err := json.Unmarshal(filter, &t.AccountFilter); err != nil {
			return nil, err
		}
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &t.PelicanConfig); err != nil {
			return nil, err
		}
	}
	t.PlanIDs = []int64(planIDs)
	if t.PlanIDs == nil {
		t.PlanIDs = []int64{}
	}
	return t, nil
}

func marshalQualityRuleAccountFilter(filter service.QualityRuleAccountFilter) (string, error) {
	data, err := json.Marshal(filter)
	return string(data), err
}

func (r *qualityRuleTemplateRepository) List(ctx context.Context) ([]*service.QualityRuleTemplate, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+qualityRuleTemplateColumns+` FROM quality_rule_templates t ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	templates := []*service.QualityRuleTemplate{}
	for rows.Next() {
		t, err := scanQualityRuleTemplate(rows)
		if err != nil {
			return nil, err
		}
		templates = append(templates, t)
	}
	return templates, rows.Err()
}

func (r *qualityRuleTemplateRepository) Get(ctx context.Context, id int64) (*service.QualityRuleTemplate, error) {
	t, err := scanQualityRuleTemplate(r.db.QueryRowContext(ctx,
		`SELECT `+qualityRuleTemplateColumns+` FROM quality_rule_templates t WHERE t.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrQualityTemplateNotFound
	}
	return t, err
}

func (r *qualityRuleTemplateRepository) Create(ctx context.Context, t *service.QualityRuleTemplate) (*service.QualityRuleTemplate, error) {
	if err := validateQualityGroupReferences(ctx, r.db, t.PelicanConfig); err != nil {
		return nil, err
	}
	filter, err := marshalQualityRuleAccountFilter(t.AccountFilter)
	if err != nil {
		return nil, err
	}
	var id int64
	if err := r.db.QueryRowContext(ctx, `
		INSERT INTO quality_rule_templates (account_filter, model_id, cron_expression, enabled, max_results, pelican_config)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, filter, t.ModelID, t.CronExpression, t.Enabled, t.MaxResults, marshalPelicanConfig(t.PelicanConfig)).Scan(&id); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// Update bumps updated_at, which makes any sync still holding the old version skip its accounts.
func (r *qualityRuleTemplateRepository) Update(ctx context.Context, t *service.QualityRuleTemplate) (*service.QualityRuleTemplate, error) {
	if err := validateQualityGroupReferences(ctx, r.db, t.PelicanConfig); err != nil {
		return nil, err
	}
	filter, err := marshalQualityRuleAccountFilter(t.AccountFilter)
	if err != nil {
		return nil, err
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE quality_rule_templates
		SET account_filter = $2, model_id = $3, cron_expression = $4, enabled = $5, max_results = $6,
			pelican_config = $7, updated_at = NOW()
		WHERE id = $1
	`, t.ID, filter, t.ModelID, t.CronExpression, t.Enabled, t.MaxResults, marshalPelicanConfig(t.PelicanConfig))
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, service.ErrQualityTemplateNotFound
	}
	return r.Get(ctx, t.ID)
}

// Delete locks the template first so a sync in flight either finishes its plan (and it is
// returned here) or sees the template gone and skips.
func (r *qualityRuleTemplateRepository) Delete(ctx context.Context, id int64) (planIDs []int64, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var locked int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM quality_rule_templates WHERE id = $1 FOR UPDATE`, id).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		err = service.ErrQualityTemplateNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT plan_id FROM quality_rule_template_accounts
		WHERE template_id = $1 AND plan_id IS NOT NULL
		ORDER BY plan_id
	`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var planID int64
		if err = rows.Scan(&planID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		planIDs = append(planIDs, planID)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM quality_rule_templates WHERE id = $1`, id); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return planIDs, nil
}

// MatchingAccounts reuses the admin account list predicates so the selector's count and the
// accounts a group rule covers agree on native status. Normal-only rules also
// exclude known exhausted OAuth snapshots without changing the admin list.
func (r *qualityRuleTemplateRepository) MatchingAccounts(ctx context.Context, filter service.QualityRuleAccountFilter) ([]service.QualityTemplateAccount, error) {
	accounts := &accountRepository{client: r.client}
	rows, err := accounts.accountListFilteredQuery("", filter.Type, strings.Join(filter.Statuses, ","), filter.Search, service.QualityTemplateGroupID(filter), "").
		Order(dbent.Asc(dbaccount.FieldID)).
		Select(dbaccount.FieldID, dbaccount.FieldPlatform, dbaccount.FieldType, dbaccount.FieldExtra, dbaccount.FieldCredentials, dbaccount.FieldRateLimitResetAt).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.QualityTemplateAccount, 0, len(rows))
	now := time.Now()
	for _, row := range rows {
		if !service.QualityTemplateQuotaAvailable(filter, accountEntityToService(row), now) {
			continue
		}
		out = append(out, service.QualityTemplateAccount{ID: row.ID, Platform: row.Platform, Type: row.Type, Extra: row.Extra})
	}
	return out, nil
}

func (r *qualityRuleTemplateRepository) LinkedAccountIDs(ctx context.Context, id int64) (map[int64]struct{}, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT account_id FROM quality_rule_template_accounts WHERE template_id = $1`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	linked := map[int64]struct{}{}
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil {
			return nil, err
		}
		linked[accountID] = struct{}{}
	}
	return linked, rows.Err()
}

// CreateLinkedPlan links the account and creates its plan atomically. The template must still
// be enabled at the version the caller read; the link must be new; and the account must not
// already hold a rule of the same kind (unique index). Otherwise nothing is written.
func (r *qualityRuleTemplateRepository) CreateLinkedPlan(ctx context.Context, t *service.QualityRuleTemplate, plan *service.ScheduledTestPlan) (created *service.ScheduledTestPlan, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var current int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM quality_rule_templates WHERE id = $1 AND enabled AND updated_at = $2 FOR SHARE
	`, t.ID, t.UpdatedAt).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		err = service.ErrQualityTemplateSkipped
	}
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO quality_rule_template_accounts (template_id, account_id)
		SELECT $1, id FROM accounts WHERE id = $2 AND deleted_at IS NULL
		ON CONFLICT DO NOTHING
	`, t.ID, plan.AccountID)
	if err != nil {
		return nil, err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		err = rowsErr
		return nil, err
	} else if n == 0 {
		err = service.ErrQualityTemplateSkipped
		return nil, err
	}
	created, err = insertScheduledTestPlan(ctx, tx, plan)
	if isUniqueViolation(err) {
		err = service.ErrQualityTemplateSkipped
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE quality_rule_template_accounts SET plan_id = $3 WHERE template_id = $1 AND account_id = $2
	`, t.ID, plan.AccountID, created.ID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return created, nil
}

// MarkSynced leaves updated_at alone so it never invalidates a concurrent sync.
func (r *qualityRuleTemplateRepository) MarkSynced(ctx context.Context, id int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE quality_rule_templates SET last_synced_at = $2 WHERE id = $1`, id, at)
	return err
}
