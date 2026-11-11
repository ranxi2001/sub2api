package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type OpenAIAPIKeyFallbackConfig struct {
	Enabled       bool    `json:"enabled"`
	AccountIDs    []int64 `json:"account_ids"`
	Metric        string  `json:"metric"`
	Threshold     float64 `json:"threshold"`
	WindowMinutes int     `json:"window_minutes"`
}

func (c *OpenAIAPIKeyFallbackConfig) UnmarshalJSON(data []byte) error {
	type configJSON OpenAIAPIKeyFallbackConfig
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"metric", "threshold", "window_minutes"} {
		if raw, ok := fields[key]; ok && string(raw) == "null" {
			return errors.New("fallback metric, threshold and window cannot be null")
		}
	}
	return json.Unmarshal(data, (*configJSON)(c))
}

func DefaultOpenAIAPIKeyFallbackConfig() OpenAIAPIKeyFallbackConfig {
	return OpenAIAPIKeyFallbackConfig{AccountIDs: []int64{}, Metric: "sla", Threshold: 95, WindowMinutes: 5}
}

func (c OpenAIAPIKeyFallbackConfig) Validate() error {
	bad := func(message string) error { return infraerrors.BadRequest("OPENAI_APIKEY_FALLBACK_INVALID", message) }
	if c.Metric != "sla" && c.Metric != "health_score" {
		return bad("select sla or health_score")
	}
	if math.IsNaN(c.Threshold) || math.IsInf(c.Threshold, 0) || c.Threshold < 0 || c.Threshold > 100 {
		return bad("OpenAI fallback threshold must be a finite number between 0 and 100")
	}
	if c.WindowMinutes < 1 || c.WindowMinutes > 1440 {
		return bad("OpenAI fallback window must be between 1 and 1440 minutes")
	}
	if len(c.AccountIDs) > 100 || c.Enabled && len(c.AccountIDs) == 0 {
		return bad("select between 1 and 100 fallback accounts before enabling")
	}
	seen := make(map[int64]bool, len(c.AccountIDs))
	for _, id := range c.AccountIDs {
		if id <= 0 || seen[id] {
			return bad("fallback account IDs must be positive and unique")
		}
		seen[id] = true
	}
	return nil
}

type OpenAIAPIKeyFallbackStatus struct {
	LastCheckedAt       *time.Time `json:"last_checked_at,omitempty"`
	OAuthAccountCount   int        `json:"oauth_account_count"`
	OAuthAvailableCount int        `json:"oauth_available_count"`
	Metric              string     `json:"metric"`
	MetricValue         *float64   `json:"metric_value,omitempty"`
	Samples             int64      `json:"samples"`
	WindowMinutes       int        `json:"window_minutes"`
	LastMetricCheckedAt *time.Time `json:"last_metric_checked_at,omitempty"`
	LastTriggerReason   string     `json:"last_trigger_reason,omitempty"`
	LastEnabledAt       *time.Time `json:"last_enabled_at,omitempty"`
	LastErrorCategory   string     `json:"last_error_category,omitempty"`
}

type openAIAPIKeyFallbackSLAReader interface {
	GetOpenAIAPIKeyFallbackMetric(context.Context, string, time.Time, time.Time) (*OpenAIAPIKeyFallbackObservation, error)
}
type OpenAIAPIKeyFallbackObservation struct {
	Value   float64
	Samples int64
}

type openAIAPIKeyFallbackReader struct {
	reader   openAIAPIKeyFallbackSLAReader
	settings *SettingService
}

// The write rechecks the selected account's platform, type, status and disabled
// switch atomically, rather than trusting an earlier inventory snapshot.
type OpenAIAPIKeyFallbackRepository interface {
	EnableOpenAIAPIKeyFallback(context.Context, int64, string) (bool, error)
}

func (s *AccountOpsService) setOpenAIAPIKeyFallbackSLAReader(reader openAIAPIKeyFallbackSLAReader, settings *SettingService) {
	if s == nil {
		return
	}
	s.fallbackSLAReader.Store(openAIAPIKeyFallbackReader{reader: reader, settings: settings})
	// Ops is constructed after AccountOps. Complete the full node's first SLA
	// check as soon as its reader exists, without waiting for request traffic.
	if s.fallbackSharedJobs.Load() {
		s.evaluateOpenAIAPIKeyFallback(context.Background())
	}
}

func (s *AccountOpsService) OpenAIAPIKeyFallbackStatus() OpenAIAPIKeyFallbackStatus {
	if s != nil {
		if status, ok := s.fallbackStatus.Load().(OpenAIAPIKeyFallbackStatus); ok {
			return status
		}
	}
	return OpenAIAPIKeyFallbackStatus{}
}

func validOpenAIAPIKeyFallbackAccount(account *Account) bool {
	return account != nil && account.ID > 0 && account.Platform == PlatformOpenAI &&
		account.Type == AccountTypeAPIKey && account.IsActive() && !account.IsShadow() &&
		(account.ExpiresAt == nil || account.ExpiresAt.After(time.Now()))
}

func (s *AccountOpsService) validateOpenAIAPIKeyFallbackAccounts(ctx context.Context, c OpenAIAPIKeyFallbackConfig) error {
	// Stale selections must not prevent an administrator from turning this off.
	if !c.Enabled {
		return nil
	}
	if s.accounts == nil {
		return infraerrors.ServiceUnavailable("OPENAI_APIKEY_FALLBACK_UNAVAILABLE", "account repository unavailable")
	}
	accounts, err := s.accounts.GetByIDs(ctx, c.AccountIDs)
	if err != nil {
		return err
	}
	valid := make(map[int64]bool, len(accounts))
	for _, account := range accounts {
		if validOpenAIAPIKeyFallbackAccount(account) {
			valid[account.ID] = true
		}
	}
	for _, id := range c.AccountIDs {
		if !valid[id] {
			return infraerrors.BadRequest("OPENAI_APIKEY_FALLBACK_ACCOUNT_INVALID", "select active, unexpired OpenAI API Key accounts without a shadow parent")
		}
	}
	return nil
}

func (s *AccountOpsService) evaluateOpenAIAPIKeyFallback(ctx context.Context) {
	if s == nil || !s.fallbackSharedJobs.Load() {
		return
	}
	// Serialize evaluation with local configuration saves, including disable.
	s.autoConfigMu.Lock()
	defer s.autoConfigMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cfg, err := s.GetOAuthAutoConfig(ctx)
	if err != nil || !cfg.OpenAIAPIKeyFallback.Enabled {
		return
	}
	c := cfg.OpenAIAPIKeyFallback
	now := time.Now().UTC()
	previous := s.OpenAIAPIKeyFallbackStatus()
	state := OpenAIAPIKeyFallbackStatus{LastCheckedAt: &now, LastTriggerReason: previous.LastTriggerReason, LastEnabledAt: previous.LastEnabledAt}
	defer func() { s.fallbackStatus.Store(state) }()
	if s.accounts == nil {
		state.LastErrorCategory = "accounts_unavailable"
		return
	}
	accounts, accountErr := s.accounts.ListAllWithFilters(ctx, PlatformOpenAI, AccountTypeOAuth, "", "", 0, "")
	if accountErr != nil {
		state.LastErrorCategory = "accounts_unavailable"
	}
	dependency, _ := s.fallbackSLAReader.Load().(openAIAPIKeyFallbackReader)
	for i := range accounts {
		account := &accounts[i]
		if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth || account.IsShadow() {
			continue
		}
		state.OAuthAccountCount++
		if openAIAPIKeyFallbackOAuthAvailable(ctx, account, dependency.settings, now) {
			state.OAuthAvailableCount++
		}
	}
	reason := ""
	if accountErr == nil && state.OAuthAccountCount > 0 && state.OAuthAvailableCount == 0 {
		reason = "oauth_unavailable"
	}
	state.Metric, state.WindowMinutes = c.Metric, c.WindowMinutes
	if previous.Metric == c.Metric && previous.WindowMinutes == c.WindowMinutes {
		state.MetricValue, state.Samples, state.LastMetricCheckedAt = previous.MetricValue, previous.Samples, previous.LastMetricCheckedAt
	}
	// A known empty OAuth pool is sufficient on its own. A slow/unavailable
	// monitoring query must not consume the write deadline for that condition.
	if reason == "" {
		if dependency, ok := s.fallbackSLAReader.Load().(openAIAPIKeyFallbackReader); ok && dependency.reader != nil &&
			(state.LastMetricCheckedAt == nil || now.Sub(*state.LastMetricCheckedAt) >= time.Minute) {
			state.LastMetricCheckedAt, state.MetricValue, state.Samples = &now, nil, 0
			observation, readErr := dependency.reader.GetOpenAIAPIKeyFallbackMetric(ctx, c.Metric, now.Add(-time.Duration(c.WindowMinutes)*time.Minute), now)
			if readErr != nil {
				state.LastErrorCategory = "metric_unavailable"
			} else if observation != nil && observation.Samples > 0 && !math.IsNaN(observation.Value) && !math.IsInf(observation.Value, 0) && observation.Value >= 0 && observation.Value <= 100 {
				state.MetricValue, state.Samples = &observation.Value, observation.Samples
			}
		}
		if state.MetricValue != nil && state.Samples > 0 && *state.MetricValue < c.Threshold {
			reason = "metric_below_threshold"
		}
	}
	if reason == "" {
		return
	}
	state.LastTriggerReason = reason
	writer, ok := s.accounts.(OpenAIAPIKeyFallbackRepository)
	if !ok {
		state.LastErrorCategory = "account_enable_failed"
		return
	}
	// Read selected accounts independently: an OAuth inventory read failure does
	// not invalidate a separately observed low SLA, and only chosen IDs may open.
	targets, err := s.accounts.GetByIDs(ctx, c.AccountIDs)
	if err != nil {
		state.LastErrorCategory = "accounts_unavailable"
		return
	}
	selected := make(map[int64]bool, len(c.AccountIDs))
	for _, id := range c.AccountIDs {
		selected[id] = true
	}
	for _, account := range targets {
		if !validOpenAIAPIKeyFallbackAccount(account) || !selected[account.ID] || account.Schedulable {
			continue
		}
		changed, err := writer.EnableOpenAIAPIKeyFallback(ctx, account.ID, cfg.Revision)
		if err != nil {
			state.LastErrorCategory = "account_enable_failed"
			continue
		}
		if !changed {
			continue
		}
		state.LastEnabledAt = &now
		slog.Info("openai_apikey_fallback_enabled", "account_id", account.ID, "reason", reason)
	}
}

func (s *OpsService) GetOpenAIAPIKeyFallbackMetric(ctx context.Context, metric string, start, end time.Time) (*OpenAIAPIKeyFallbackObservation, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	filter := &OpsDashboardFilter{StartTime: start, EndTime: end, Platform: PlatformOpenAI, OpenAIOAuthOnly: true, QueryMode: OpsQueryModeRaw}
	if metric == "health_score" {
		// Only request health of the OAuth pool can open fallback accounts.
		// Global infrastructure and background jobs are not OAuth failures.
		overview, err := s.opsRepo.GetDashboardOverview(ctx, filter)
		if err != nil || overview == nil {
			return nil, err
		}
		scoped := *overview
		scoped.SystemMetrics, scoped.JobHeartbeats = nil, nil
		return &OpenAIAPIKeyFallbackObservation{Value: float64(computeDashboardHealthScore(end, &scoped)), Samples: scoped.RequestCountSLA}, nil
	}
	stats, err := s.opsRepo.GetWindowStats(ctx, filter)
	if err != nil || stats == nil {
		return nil, err
	}
	return &OpenAIAPIKeyFallbackObservation{Value: stats.SLA * 100, Samples: stats.RequestCountSLA}, nil
}
