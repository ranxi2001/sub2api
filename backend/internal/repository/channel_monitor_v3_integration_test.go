//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestChannelMonitorV3RepositoryLayout(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewChannelMonitorV3Repository(integrationDB)
	original, err := repo.GetConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, 5, original.IntervalMinutes, "migration defaults")
	require.Equal(t, 90, original.Cells)
	require.Equal(t, "7d", original.AvailabilityRange)
	require.InDelta(t, 0.2, original.DownErrorRate, 1e-9)
	require.ElementsMatch(t, service.DefaultChannelMonitorV2IgnoredErrorCategories, original.IgnoredErrorCategories)

	group := mustCreateGroup(t, client, &service.Group{Name: "v3-" + uuid.NewString(), Platform: service.PlatformOpenAI, RateMultiplier: 0.2})
	t.Cleanup(func() {
		current, loadErr := repo.GetConfig(ctx)
		require.NoError(t, loadErr)
		restore := *original
		restore.FeaturedComponentID = nil
		_, restoreErr := repo.UpdateConfig(ctx, restore, current.Version, 0)
		require.NoError(t, restoreErr)
		_, cleanupErr := integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id = $1`, group.ID)
		require.NoError(t, cleanupErr)
		_, cleanupErr = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_v3_categories WHERE name LIKE 'v3-it-%'`)
		require.NoError(t, cleanupErr)
	})

	gpt, err := repo.CreateCategory(ctx, service.ChannelMonitorV3CategoryInput{Name: "v3-it-gpt", Description: "OpenAI"})
	require.NoError(t, err)
	claude, err := repo.CreateCategory(ctx, service.ChannelMonitorV3CategoryInput{Name: "v3-it-claude"})
	require.NoError(t, err)
	require.Greater(t, claude.SortOrder, gpt.SortOrder)

	input := service.ChannelMonitorV3ComponentInput{CategoryID: &gpt.ID, Name: "Codex", GroupID: group.ID, Model: "gpt-5.5",
		DegradedTTFTMs: 15000, ShowMultiplier: true, Visibility: service.ChannelMonitorV3VisibilityGroup, Enabled: true}
	codex, err := repo.CreateComponent(ctx, input)
	require.NoError(t, err)
	require.Equal(t, group.Name, codex.GroupName)
	require.Equal(t, service.PlatformOpenAI, codex.GroupPlatform)
	require.InDelta(t, 0.2, codex.GroupRateMultiplier, 1e-9)
	require.Equal(t, 15000, codex.DegradedTTFTMs)
	require.Equal(t, []string{input.Model}, codex.Models)
	selected := input
	selected.Models = []string{input.Model, "test-astra"}
	codex, err = repo.UpdateComponent(ctx, codex.ID, selected)
	require.NoError(t, err)
	require.Equal(t, selected.Models, codex.Models)
	codex, err = repo.UpdateComponent(ctx, codex.ID, input)
	require.NoError(t, err)
	require.Equal(t, selected.Models, codex.Models, "legacy edit of an unrelated field preserves the selection")
	selected.Models, selected.Model = []string{}, ""
	codex, err = repo.UpdateComponent(ctx, codex.ID, selected)
	require.NoError(t, err)
	require.Empty(t, codex.Models)
	featuredInput := input
	featuredInput.CategoryID, featuredInput.Name, featuredInput.Model = nil, "Featured", ""
	featured, err := repo.CreateComponent(ctx, featuredInput)
	require.NoError(t, err)

	cfg := *original
	cfg.IntervalMinutes, cfg.FeaturedComponentID, cfg.FooterNote = 3, &featured.ID, "note"
	cfg.DownErrorRate, cfg.DegradedErrorRate, cfg.IgnoredErrorCategories = 0.3, 0.1, []string{"timeout"}
	updated, err := repo.UpdateConfig(ctx, cfg, original.Version, 7)
	require.NoError(t, err)
	require.Equal(t, original.Version+1, updated.Version)
	require.Equal(t, featured.ID, *updated.FeaturedComponentID)
	require.InDelta(t, 0.3, updated.DownErrorRate, 1e-9)
	require.Equal(t, []string{"timeout"}, updated.IgnoredErrorCategories)
	stale, err := repo.UpdateConfig(ctx, cfg, original.Version, 7)
	require.NoError(t, err)
	require.Nil(t, stale, "a stale version must not overwrite newer settings")

	require.NoError(t, repo.Reorder(ctx, []int64{claude.ID, gpt.ID}, []int64{featured.ID, codex.ID}))
	categories, err := repo.ListCategories(ctx)
	require.NoError(t, err)
	var order []int64
	for _, category := range categories {
		if category.ID == gpt.ID || category.ID == claude.ID {
			order = append(order, category.ID)
		}
	}
	require.Equal(t, []int64{claude.ID, gpt.ID}, order)

	deleted, err := repo.DeleteCategory(ctx, gpt.ID)
	require.NoError(t, err)
	require.True(t, deleted)
	codex, err = repo.GetComponent(ctx, codex.ID)
	require.NoError(t, err)
	require.Nil(t, codex.CategoryID, "components outlive their category")

	deleted, err = repo.DeleteComponent(ctx, featured.ID)
	require.NoError(t, err)
	require.True(t, deleted)
	after, err := repo.GetConfig(ctx)
	require.NoError(t, err)
	require.Nil(t, after.FeaturedComponentID, "deleting the featured component clears the card")
	missing, err := repo.UpdateComponent(ctx, featured.ID, input)
	require.NoError(t, err)
	require.Nil(t, missing)
}

func TestChannelMonitorV3RepositoryReadsPassiveFacts(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewChannelMonitorV3Repository(integrationDB)
	group := mustCreateGroup(t, client, &service.Group{Name: "v3-facts-" + uuid.NewString(), Platform: service.PlatformOpenAI})
	other := mustCreateGroup(t, client, &service.Group{Name: "v3-other-" + uuid.NewString(), Platform: service.PlatformOpenAI})
	ids := []any{group.ID, other.ID}
	t.Cleanup(func() {
		for _, table := range []string{"channel_monitor_v2_metrics_1m", "channel_monitor_v2_error_metrics_1m", "channel_monitor_v2_latency_histograms_1m",
			"channel_monitor_v2_metrics_rollup", "channel_monitor_v2_error_metrics_rollup"} {
			_, err := integrationDB.ExecContext(ctx, `DELETE FROM `+table+` WHERE group_id IN ($1, $2)`, ids...)
			require.NoError(t, err)
		}
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id IN ($1, $2)`, ids...)
		require.NoError(t, err)
	})

	base := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := integrationDB.ExecContext(ctx, query, args...)
		require.NoError(t, err)
	}
	metric := `INSERT INTO channel_monitor_v2_metrics_1m (bucket_start, platform, group_id, model, success_requests, error_requests) VALUES ($1, 'openai', $2, $3, $4, $5)`
	exec(metric, base, group.ID, "gpt-5.5", 10, 1)
	exec(metric, base.Add(4*time.Minute), group.ID, "gpt-5.5", 5, 4)
	exec(metric, base.Add(4*time.Minute), group.ID, "gpt-5.5-mini", 2, 0)
	exec(metric, base.Add(5*time.Minute), group.ID, "gpt-5.5", 7, 0)
	exec(metric, base, other.ID, "gpt-5.5", 100, 100)
	exec(`INSERT INTO channel_monitor_v2_error_metrics_1m (bucket_start, platform, group_id, model, error_category, taxonomy_version, error_requests)
		VALUES ($1, 'openai', $2, 'gpt-5.5', 'upstream_5xx', $3, 3), ($1, 'openai', $2, 'gpt-5.5', 'timeout', $3, 2), ($1, 'openai', $2, 'gpt-5.5', 'other', 99, 50)`,
		base.Add(time.Minute), group.ID, service.ChannelMonitorV2TaxonomyVersion)
	exec(`INSERT INTO channel_monitor_v2_latency_histograms_1m (bucket_start, platform, group_id, model, user_id, metric, upper_bound_ms, sample_count)
		VALUES ($1, 'openai', $2, 'gpt-5.5', 0, 'ttft', 2000, 6), ($1, 'openai', $2, 'gpt-5.5', 0, 'duration', 5000, 6),
		       ($1, 'openai', $2, 'gpt-5.5', 42, 'ttft', 2000, 6), ($3, 'openai', $2, 'gpt-5.5', 0, 'ttft', 3000, 4)`,
		base.Add(2*time.Minute), group.ID, base.Add(3*time.Minute))

	facts, err := repo.SlotFacts(ctx, []int64{group.ID}, base, base.Add(10*time.Minute), 5*time.Minute, true)
	require.NoError(t, err)
	sums := map[string][2]int64{}
	for _, f := range facts.Metrics {
		require.Equal(t, group.ID, f.GroupID, "other groups are not read")
		key := f.Slot.UTC().Format("15:04") + " " + f.Model
		value := sums[key]
		sums[key] = [2]int64{value[0] + f.Success, value[1] + f.Errors}
	}
	first, second := base.Format("15:04"), base.Add(5*time.Minute).Format("15:04")
	require.Equal(t, map[string][2]int64{first + " gpt-5.5": {15, 5}, first + " gpt-5.5-mini": {2, 0}, second + " gpt-5.5": {7, 0}}, sums)
	require.Len(t, facts.Errors, 2, "only the current taxonomy is read")
	latency := map[int64]int64{}
	for _, f := range facts.Latency {
		require.Equal(t, base, f.Slot.UTC())
		latency[f.UpperBound] += f.Count
	}
	require.Equal(t, map[int64]int64{2000: 6, 3000: 4}, latency, "first-token samples of all users only")

	withoutLatency, err := repo.SlotFacts(ctx, []int64{group.ID}, base, base.Add(10*time.Minute), 5*time.Minute, false)
	require.NoError(t, err)
	require.Empty(t, withoutLatency.Latency)
	require.Len(t, withoutLatency.Metrics, 3)

	rollup := `INSERT INTO channel_monitor_v2_metrics_rollup (bucket_seconds, bucket_start, platform, group_id, model, success_requests, error_requests) VALUES ($1, $2, 'openai', $3, 'gpt-5.5', $4, $5)`
	exec(rollup, 3600, base, group.ID, 100, 10)
	exec(rollup, 3600, base.Add(time.Hour), group.ID, 50, 5)
	exec(rollup, 300, base, group.ID, 1000, 1000)
	exec(rollup, 3600, base.Add(-48*time.Hour), group.ID, 1, 1)
	exec(`INSERT INTO channel_monitor_v2_error_metrics_rollup (bucket_seconds, bucket_start, platform, group_id, model, error_category, taxonomy_version, error_requests)
		VALUES (3600, $1, 'openai', $2, 'gpt-5.5', 'client_cancelled', $3, 4)`, base, group.ID, service.ChannelMonitorV2TaxonomyVersion)
	totals, err := repo.RangeFacts(ctx, []int64{group.ID}, base.Add(-time.Hour), base.Add(3*time.Hour))
	require.NoError(t, err)
	require.Len(t, totals.Metrics, 1)
	require.Equal(t, int64(150), totals.Metrics[0].Success, "hourly rollups inside the range only")
	require.Equal(t, int64(15), totals.Metrics[0].Errors)
	require.Len(t, totals.Errors, 1)
	require.Equal(t, "client_cancelled", totals.Errors[0].Category)

	var previous sql.NullTime
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT data_through FROM channel_monitor_v2_watermarks WHERE id = 1`).Scan(&previous))
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, `UPDATE channel_monitor_v2_watermarks SET data_through = $1 WHERE id = 1`, previous)
		require.NoError(t, err)
	})
	exec(`UPDATE channel_monitor_v2_watermarks SET data_through = $1 WHERE id = 1`, base)
	through, err := repo.DataThrough(ctx)
	require.NoError(t, err)
	require.Equal(t, base, through.UTC())
	exec(`UPDATE channel_monitor_v2_watermarks SET data_through = NULL WHERE id = 1`)
	through, err = repo.DataThrough(ctx)
	require.NoError(t, err)
	require.Nil(t, through, "no aggregation has run yet")
}
