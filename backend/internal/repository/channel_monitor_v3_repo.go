package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type channelMonitorV3Repository struct{ db *sql.DB }

func NewChannelMonitorV3Repository(db *sql.DB) service.ChannelMonitorV3Repository {
	return &channelMonitorV3Repository{db: db}
}

const channelMonitorV3ConfigColumns = `version, interval_minutes, cells, availability_range, down_error_rate::float8,
	degraded_error_rate::float8, degraded_ttft_ms, min_requests, ignored_error_categories,
	featured_component_id, footer_note, updated_at, updated_by, idle_probe`

func scanChannelMonitorV3Config(row interface{ Scan(...any) error }) (*service.ChannelMonitorV3Config, error) {
	var cfg service.ChannelMonitorV3Config
	var idleProbe []byte
	if err := row.Scan(&cfg.Version, &cfg.IntervalMinutes, &cfg.Cells, &cfg.AvailabilityRange, &cfg.DownErrorRate,
		&cfg.DegradedErrorRate, &cfg.DegradedTTFTMs, &cfg.MinRequests, pq.Array(&cfg.IgnoredErrorCategories),
		&cfg.FeaturedComponentID, &cfg.FooterNote, &cfg.UpdatedAt, &cfg.UpdatedBy, &idleProbe); err != nil {
		return nil, err
	}
	if cfg.IgnoredErrorCategories == nil {
		cfg.IgnoredErrorCategories = []string{}
	}
	if err := json.Unmarshal(idleProbe, &cfg.IdleProbe); err != nil {
		return nil, fmt.Errorf("decode channel monitor v3 idle probe: %w", err)
	}
	if cfg.IdleProbe.Groups == nil {
		cfg.IdleProbe.Groups = []service.ChannelMonitorV3IdleProbeGroup{}
	}
	return &cfg, nil
}

func (r *channelMonitorV3Repository) GetConfig(ctx context.Context) (*service.ChannelMonitorV3Config, error) {
	cfg, err := scanChannelMonitorV3Config(r.db.QueryRowContext(ctx,
		`SELECT `+channelMonitorV3ConfigColumns+` FROM channel_monitor_v3_config WHERE id = 1`))
	if err != nil {
		return nil, fmt.Errorf("get channel monitor v3 config: %w", err)
	}
	return cfg, nil
}

func (r *channelMonitorV3Repository) UpdateConfig(ctx context.Context, cfg service.ChannelMonitorV3Config, expectedVersion int, updatedBy int64) (*service.ChannelMonitorV3Config, error) {
	ignored := cfg.IgnoredErrorCategories
	if ignored == nil {
		ignored = []string{}
	}
	updated, err := scanChannelMonitorV3Config(r.db.QueryRowContext(ctx, `
		UPDATE channel_monitor_v3_config
		SET version = version + 1, interval_minutes = $1, cells = $2, availability_range = $3,
		    down_error_rate = $4, degraded_error_rate = $5, degraded_ttft_ms = $6, min_requests = $7,
		    ignored_error_categories = $8, featured_component_id = $9, footer_note = $10,
		    updated_by = $11, updated_at = NOW()
		WHERE id = 1 AND version = $12
		RETURNING `+channelMonitorV3ConfigColumns,
		cfg.IntervalMinutes, cfg.Cells, cfg.AvailabilityRange, cfg.DownErrorRate, cfg.DegradedErrorRate,
		cfg.DegradedTTFTMs, cfg.MinRequests, pq.Array(ignored), cfg.FeaturedComponentID, cfg.FooterNote,
		updatedBy, expectedVersion))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update channel monitor v3 config: %w", err)
	}
	return updated, nil
}

const channelMonitorV3CategoryColumns = `id, name, description, sort_order, created_at, updated_at`

func scanChannelMonitorV3Category(row interface{ Scan(...any) error }) (*service.ChannelMonitorV3Category, error) {
	var category service.ChannelMonitorV3Category
	if err := row.Scan(&category.ID, &category.Name, &category.Description, &category.SortOrder, &category.CreatedAt, &category.UpdatedAt); err != nil {
		return nil, err
	}
	return &category, nil
}

func (r *channelMonitorV3Repository) ListCategories(ctx context.Context) ([]service.ChannelMonitorV3Category, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+channelMonitorV3CategoryColumns+`
		FROM channel_monitor_v3_categories ORDER BY sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("list channel monitor v3 categories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []service.ChannelMonitorV3Category{}
	for rows.Next() {
		category, err := scanChannelMonitorV3Category(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *category)
	}
	return out, rows.Err()
}

func (r *channelMonitorV3Repository) CreateCategory(ctx context.Context, input service.ChannelMonitorV3CategoryInput) (*service.ChannelMonitorV3Category, error) {
	category, err := scanChannelMonitorV3Category(r.db.QueryRowContext(ctx, `
		INSERT INTO channel_monitor_v3_categories (name, description, sort_order)
		VALUES ($1, $2, COALESCE((SELECT MAX(sort_order) + 1 FROM channel_monitor_v3_categories), 0))
		RETURNING `+channelMonitorV3CategoryColumns, input.Name, input.Description))
	if err != nil {
		return nil, fmt.Errorf("create channel monitor v3 category: %w", err)
	}
	return category, nil
}

func (r *channelMonitorV3Repository) UpdateCategory(ctx context.Context, id int64, input service.ChannelMonitorV3CategoryInput) (*service.ChannelMonitorV3Category, error) {
	category, err := scanChannelMonitorV3Category(r.db.QueryRowContext(ctx, `
		UPDATE channel_monitor_v3_categories SET name = $2, description = $3, updated_at = NOW()
		WHERE id = $1 RETURNING `+channelMonitorV3CategoryColumns, id, input.Name, input.Description))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update channel monitor v3 category: %w", err)
	}
	return category, nil
}

func (r *channelMonitorV3Repository) DeleteCategory(ctx context.Context, id int64) (bool, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM channel_monitor_v3_categories WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("delete channel monitor v3 category: %w", err)
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

const channelMonitorV3ComponentSelect = `
	SELECT c.id, c.category_id, c.name, c.description, c.group_id, c.model,
	       CASE WHEN c.models[1] = c.model THEN c.models WHEN c.model <> '' THEN ARRAY[c.model] ELSE '{}'::text[] END, c.degraded_ttft_ms,
	       c.show_multiplier, c.visibility, c.enabled, c.sort_order, c.created_at, c.updated_at,
	       COALESCE(g.name, ''), COALESCE(g.platform, ''), COALESCE(g.status, ''),
	       COALESCE(g.rate_multiplier, 0)::float8, (g.id IS NULL OR g.deleted_at IS NOT NULL)
	FROM channel_monitor_v3_components c
	LEFT JOIN groups g ON g.id = c.group_id`

func scanChannelMonitorV3Component(row interface{ Scan(...any) error }) (*service.ChannelMonitorV3Component, error) {
	var c service.ChannelMonitorV3Component
	if err := row.Scan(&c.ID, &c.CategoryID, &c.Name, &c.Description, &c.GroupID, &c.Model, pq.Array(&c.Models), &c.DegradedTTFTMs,
		&c.ShowMultiplier, &c.Visibility, &c.Enabled, &c.SortOrder, &c.CreatedAt, &c.UpdatedAt,
		&c.GroupName, &c.GroupPlatform, &c.GroupStatus, &c.GroupRateMultiplier, &c.GroupDeleted); err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *channelMonitorV3Repository) ListComponents(ctx context.Context) ([]service.ChannelMonitorV3Component, error) {
	rows, err := r.db.QueryContext(ctx, channelMonitorV3ComponentSelect+` ORDER BY c.sort_order, c.id`)
	if err != nil {
		return nil, fmt.Errorf("list channel monitor v3 components: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []service.ChannelMonitorV3Component{}
	for rows.Next() {
		component, err := scanChannelMonitorV3Component(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *component)
	}
	return out, rows.Err()
}

func (r *channelMonitorV3Repository) GetComponent(ctx context.Context, id int64) (*service.ChannelMonitorV3Component, error) {
	component, err := scanChannelMonitorV3Component(r.db.QueryRowContext(ctx, channelMonitorV3ComponentSelect+` WHERE c.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get channel monitor v3 component: %w", err)
	}
	return component, nil
}

func (r *channelMonitorV3Repository) CreateComponent(ctx context.Context, input service.ChannelMonitorV3ComponentInput) (*service.ChannelMonitorV3Component, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO channel_monitor_v3_components (category_id, name, description, group_id, model,
			degraded_ttft_ms, show_multiplier, visibility, enabled, models, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, COALESCE($10::text[], '{}'),
			COALESCE((SELECT MAX(sort_order) + 1 FROM channel_monitor_v3_components), 0))
		RETURNING id`,
		input.CategoryID, input.Name, input.Description, input.GroupID, input.Model,
		input.DegradedTTFTMs, input.ShowMultiplier, input.Visibility, input.Enabled, pq.Array(input.Models)).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("create channel monitor v3 component: %w", err)
	}
	return r.GetComponent(ctx, id)
}

func (r *channelMonitorV3Repository) UpdateComponent(ctx context.Context, id int64, input service.ChannelMonitorV3ComponentInput) (*service.ChannelMonitorV3Component, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE channel_monitor_v3_components
		SET category_id = $2, name = $3, description = $4, group_id = $5, model = $6::text,
		    degraded_ttft_ms = $7, show_multiplier = $8, visibility = $9, enabled = $10, updated_at = NOW(),
		    models = COALESCE($11::text[], CASE WHEN group_id = $5 AND model = $6::text THEN models ELSE '{}'::text[] END)
		WHERE id = $1`,
		id, input.CategoryID, input.Name, input.Description, input.GroupID, input.Model,
		input.DegradedTTFTMs, input.ShowMultiplier, input.Visibility, input.Enabled, pq.Array(input.Models))
	if err != nil {
		return nil, fmt.Errorf("update channel monitor v3 component: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		return nil, err
	}
	return r.GetComponent(ctx, id)
}

func (r *channelMonitorV3Repository) DeleteComponent(ctx context.Context, id int64) (bool, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM channel_monitor_v3_components WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("delete channel monitor v3 component: %w", err)
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func (r *channelMonitorV3Repository) Reorder(ctx context.Context, categoryIDs, componentIDs []int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, step := range []struct {
		table string
		ids   []int64
	}{{"channel_monitor_v3_categories", categoryIDs}, {"channel_monitor_v3_components", componentIDs}} {
		if len(step.ids) == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE `+step.table+` t SET sort_order = o.position, updated_at = NOW()
			FROM unnest($1::bigint[]) WITH ORDINALITY AS o(id, position) WHERE t.id = o.id`, pq.Array(step.ids)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// channelMonitorV3SlotSQL bins V2 minute facts the way time.Truncate does for
// intervals that divide a day.
const channelMonitorV3SlotSQL = `date_bin($4::interval, %s.bucket_start, ` + channelMonitorV2DateBinOrigin + `)`

func (r *channelMonitorV3Repository) SlotFacts(ctx context.Context, groupIDs []int64, from, to time.Time, interval time.Duration, withLatency bool) (*service.ChannelMonitorV3Facts, error) {
	facts := &service.ChannelMonitorV3Facts{}
	if len(groupIDs) == 0 {
		return facts, nil
	}
	args := []any{pq.Array(groupIDs), from, to, fmt.Sprintf("%d seconds", int(interval.Seconds()))}
	rows, err := r.db.QueryContext(ctx, `
		SELECT m.group_id, m.model, `+fmt.Sprintf(channelMonitorV3SlotSQL, "m")+`, SUM(m.success_requests), SUM(m.error_requests)
		FROM channel_monitor_v2_metrics_1m m
		WHERE m.group_id = ANY($1) AND m.bucket_start >= $2 AND m.bucket_start < $3
		GROUP BY 1, 2, 3`, args...)
	if err != nil {
		return nil, fmt.Errorf("load channel monitor v3 metrics: %w", err)
	}
	if err := scanChannelMonitorV3Rows(rows, func(rows *sql.Rows) error {
		var f service.ChannelMonitorV3Fact
		if err := rows.Scan(&f.GroupID, &f.Model, &f.Slot, &f.Success, &f.Errors); err != nil {
			return err
		}
		facts.Metrics = append(facts.Metrics, f)
		return nil
	}); err != nil {
		return nil, err
	}
	rows, err = r.db.QueryContext(ctx, `
		SELECT e.group_id, e.model, `+fmt.Sprintf(channelMonitorV3SlotSQL, "e")+`, e.error_category, SUM(e.error_requests)
		FROM channel_monitor_v2_error_metrics_1m e
		WHERE e.group_id = ANY($1) AND e.bucket_start >= $2 AND e.bucket_start < $3 AND e.taxonomy_version = $5
		GROUP BY 1, 2, 3, 4`, append(args, service.ChannelMonitorV2TaxonomyVersion)...)
	if err != nil {
		return nil, fmt.Errorf("load channel monitor v3 errors: %w", err)
	}
	if err := scanChannelMonitorV3Rows(rows, func(rows *sql.Rows) error {
		var f service.ChannelMonitorV3ErrorFact
		if err := rows.Scan(&f.GroupID, &f.Model, &f.Slot, &f.Category, &f.Errors); err != nil {
			return err
		}
		facts.Errors = append(facts.Errors, f)
		return nil
	}); err != nil {
		return nil, err
	}
	if !withLatency {
		return facts, nil
	}
	// user_id 0 rows hold every user's samples.
	rows, err = r.db.QueryContext(ctx, `
		SELECT h.group_id, h.model, `+fmt.Sprintf(channelMonitorV3SlotSQL, "h")+`, h.upper_bound_ms, SUM(h.sample_count)
		FROM channel_monitor_v2_latency_histograms_1m h
		WHERE h.group_id = ANY($1) AND h.bucket_start >= $2 AND h.bucket_start < $3 AND h.user_id = 0 AND h.metric = 'ttft'
		GROUP BY 1, 2, 3, 4`, args...)
	if err != nil {
		return nil, fmt.Errorf("load channel monitor v3 latency: %w", err)
	}
	if err := scanChannelMonitorV3Rows(rows, func(rows *sql.Rows) error {
		var f service.ChannelMonitorV3LatencyFact
		if err := rows.Scan(&f.GroupID, &f.Model, &f.Slot, &f.UpperBound, &f.Count); err != nil {
			return err
		}
		facts.Latency = append(facts.Latency, f)
		return nil
	}); err != nil {
		return nil, err
	}
	return facts, nil
}

// RangeFacts reads V2's hourly rollups, which cover 30 days.
func (r *channelMonitorV3Repository) RangeFacts(ctx context.Context, groupIDs []int64, from, to time.Time) (*service.ChannelMonitorV3Facts, error) {
	facts := &service.ChannelMonitorV3Facts{}
	if len(groupIDs) == 0 {
		return facts, nil
	}
	args := []any{pq.Array(groupIDs), from, to}
	rows, err := r.db.QueryContext(ctx, `
		SELECT m.group_id, m.model, SUM(m.success_requests), SUM(m.error_requests)
		FROM channel_monitor_v2_metrics_rollup m
		WHERE m.bucket_seconds = 3600 AND m.group_id = ANY($1) AND m.bucket_start >= $2 AND m.bucket_start < $3
		GROUP BY 1, 2`, args...)
	if err != nil {
		return nil, fmt.Errorf("load channel monitor v3 totals: %w", err)
	}
	if err := scanChannelMonitorV3Rows(rows, func(rows *sql.Rows) error {
		var f service.ChannelMonitorV3Fact
		if err := rows.Scan(&f.GroupID, &f.Model, &f.Success, &f.Errors); err != nil {
			return err
		}
		facts.Metrics = append(facts.Metrics, f)
		return nil
	}); err != nil {
		return nil, err
	}
	rows, err = r.db.QueryContext(ctx, `
		SELECT e.group_id, e.model, e.error_category, SUM(e.error_requests)
		FROM channel_monitor_v2_error_metrics_rollup e
		WHERE e.bucket_seconds = 3600 AND e.group_id = ANY($1) AND e.bucket_start >= $2 AND e.bucket_start < $3
		  AND e.taxonomy_version = $4
		GROUP BY 1, 2, 3`, append(args, service.ChannelMonitorV2TaxonomyVersion)...)
	if err != nil {
		return nil, fmt.Errorf("load channel monitor v3 error totals: %w", err)
	}
	if err := scanChannelMonitorV3Rows(rows, func(rows *sql.Rows) error {
		var f service.ChannelMonitorV3ErrorFact
		if err := rows.Scan(&f.GroupID, &f.Model, &f.Category, &f.Errors); err != nil {
			return err
		}
		facts.Errors = append(facts.Errors, f)
		return nil
	}); err != nil {
		return nil, err
	}
	return facts, nil
}

func (r *channelMonitorV3Repository) DataThrough(ctx context.Context) (*time.Time, error) {
	var through sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT data_through FROM channel_monitor_v2_watermarks WHERE id = 1`).Scan(&through)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !through.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load channel monitor v2 watermark: %w", err)
	}
	value := through.Time.UTC()
	return &value, nil
}

func scanChannelMonitorV3Rows(rows *sql.Rows, scan func(*sql.Rows) error) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
