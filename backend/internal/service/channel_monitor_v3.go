package service

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Channel monitor V3 is an admin-curated status page over the same passive
// facts V2 aggregates from real user requests. Components map groups to rows;
// every bar is cut into the same time slots, so columns line up across bars.
// V3 sends no requests of its own, except the opt-in idle probes that fill
// slots without enough real traffic for the groups an admin checks
// (channel_monitor_v3_probe.go).

const (
	ChannelMonitorV3StatusOperational = "operational"
	ChannelMonitorV3StatusDegraded    = "degraded"
	ChannelMonitorV3StatusDown        = "down"
	// Insufficient marks a slot with fewer requests than the configured minimum.
	ChannelMonitorV3StatusInsufficient = "insufficient"
	ChannelMonitorV3StatusUnknown      = "unknown"

	ChannelMonitorV3VisibilityGroup  = "group"
	ChannelMonitorV3VisibilityPublic = "public"

	// ChannelMonitorV3History is how far back bars can page: V2 keeps its
	// one-minute facts for seven days.
	ChannelMonitorV3History = 7 * 24 * time.Hour
	// ChannelMonitorV3IncidentMinDuration is how long slots must stay down to
	// count as an incident. Shorter blips stay red on the bars but are left out
	// of the incident history.
	ChannelMonitorV3IncidentMinDuration = 10 * time.Minute
	// channelMonitorV3IncidentBridge is the longest stretch of quiet slots an
	// incident survives, so a lull in traffic does not split one outage in two.
	channelMonitorV3IncidentBridge = 30 * time.Minute

	channelMonitorV3MaxCategories = 50
	channelMonitorV3MaxComponents = 100
	channelMonitorV3IncidentPage  = 100
)

// ChannelMonitorV3Intervals are the slot lengths a page may use (minutes).
var ChannelMonitorV3Intervals = []int{1, 2, 3, 5, 10, 15, 30, 60}

// ChannelMonitorV3TTFTThresholds are V2's latency histogram bounds, so a
// threshold compares exactly against the P50 bucket.
var ChannelMonitorV3TTFTThresholds = []int{1000, 2000, 3000, 5000, 8000, 10000, 15000, 30000, 60000, 120000}

var channelMonitorV3Ranges = map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

var (
	ErrChannelMonitorV3Invalid  = infraerrors.BadRequest("CHANNEL_MONITOR_V3_INVALID", "invalid channel monitor v3 settings")
	ErrChannelMonitorV3Conflict = infraerrors.Conflict("CHANNEL_MONITOR_V3_CONFLICT", "channel monitor v3 settings were changed elsewhere; reload and try again")
	ErrChannelMonitorV3NotFound = infraerrors.NotFound("CHANNEL_MONITOR_V3_NOT_FOUND", "channel monitor v3 item not found")
)

func channelMonitorV3Invalid(format string, args ...any) error {
	return infraerrors.Newf(http.StatusBadRequest, "CHANNEL_MONITOR_V3_INVALID", format, args...)
}

type ChannelMonitorV3Config struct {
	Version           int    `json:"version"`
	IntervalMinutes   int    `json:"interval_minutes"`
	Cells             int    `json:"cells"`
	AvailabilityRange string `json:"availability_range"`
	// A slot is down at DownErrorRate, degraded at DegradedErrorRate or when
	// its first-token P50 is above DegradedTTFTMs.
	DownErrorRate     float64 `json:"down_error_rate"`
	DegradedErrorRate float64 `json:"degraded_error_rate"`
	DegradedTTFTMs    int     `json:"degraded_ttft_ms"`
	// MinRequests below which a slot is shown as too quiet to judge.
	MinRequests int `json:"min_requests"`
	// IgnoredErrorCategories are errors users cause; they never count against a channel.
	IgnoredErrorCategories []string  `json:"ignored_error_categories"`
	FeaturedComponentID    *int64    `json:"featured_component_id"`
	FooterNote             string    `json:"footer_note"`
	UpdatedAt              time.Time `json:"updated_at"`
	UpdatedBy              *int64    `json:"updated_by,omitempty"`
	// IdleProbe is read with the config but saved on its own (UpdateIdleProbe),
	// so the page settings form never carries or overwrites it.
	IdleProbe ChannelMonitorV3IdleProbe `json:"-"`
}

type ChannelMonitorV3Category struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	SortOrder   int       `json:"sort_order"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ChannelMonitorV3CategoryInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ChannelMonitorV3Component struct {
	ID          int64  `json:"id"`
	CategoryID  *int64 `json:"category_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	GroupID     int64  `json:"group_id"`
	// Model is the first selected model for legacy clients and idle probes.
	Model          string   `json:"model"`
	Models         []string `json:"models,omitempty"`
	DegradedTTFTMs int      `json:"degraded_ttft_ms"`
	ShowMultiplier bool     `json:"show_multiplier"`
	Visibility     string   `json:"visibility"`
	Enabled        bool     `json:"enabled"`
	SortOrder      int      `json:"sort_order"`
	// Joined from groups for display.
	GroupName           string    `json:"group_name"`
	GroupPlatform       string    `json:"group_platform"`
	GroupStatus         string    `json:"group_status"`
	GroupRateMultiplier float64   `json:"group_rate_multiplier"`
	GroupDeleted        bool      `json:"group_deleted"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (c *ChannelMonitorV3Component) includesModel(model string) bool {
	if len(c.Models) != 0 {
		return slices.Contains(c.Models, model)
	}
	return c.Model == "" || model == c.Model
}

type ChannelMonitorV3ComponentInput struct {
	CategoryID     *int64   `json:"category_id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	GroupID        int64    `json:"group_id"`
	Model          string   `json:"model"`
	Models         []string `json:"models"`
	DegradedTTFTMs int      `json:"degraded_ttft_ms"`
	ShowMultiplier bool     `json:"show_multiplier"`
	Visibility     string   `json:"visibility"`
	Enabled        bool     `json:"enabled"`
}

// ChannelMonitorV3Settings is everything the admin editor needs in one read.
type ChannelMonitorV3Settings struct {
	Config          ChannelMonitorV3Config      `json:"config"`
	Categories      []ChannelMonitorV3Category  `json:"categories"`
	Components      []ChannelMonitorV3Component `json:"components"`
	ErrorCategories []string                    `json:"error_categories"`
	DataThrough     *time.Time                  `json:"data_through"`
	IdleProbe       ChannelMonitorV3IdleProbe   `json:"idle_probe"`
	// IdleProbeStats covers every group probed in the last day, checked or not.
	IdleProbeStats []ChannelMonitorV3IdleProbeStat `json:"idle_probe_stats"`
}

// Passive facts read from the V2 aggregates. Slot is zero for range totals.
type ChannelMonitorV3Fact struct {
	GroupID int64
	Model   string
	Slot    time.Time
	Success int64
	Errors  int64
}

type ChannelMonitorV3ErrorFact struct {
	GroupID  int64
	Model    string
	Slot     time.Time
	Category string
	Errors   int64
}

type ChannelMonitorV3LatencyFact struct {
	GroupID    int64
	Model      string
	Slot       time.Time
	UpperBound int64
	Count      int64
}

type ChannelMonitorV3Facts struct {
	Metrics []ChannelMonitorV3Fact
	Errors  []ChannelMonitorV3ErrorFact
	Latency []ChannelMonitorV3LatencyFact
	// Probes are the idle probes of the checked groups; empty while probing is off.
	Probes []ChannelMonitorV3ProbeFact
}

type ChannelMonitorV3Cell struct {
	Start       time.Time `json:"start"`
	Status      string    `json:"status"`
	SuccessRate float64   `json:"success_rate"`
	TTFTP50Ms   *int64    `json:"ttft_p50_ms,omitempty"`
	TopError    string    `json:"top_error,omitempty"`
	// Probed marks a slot judged by idle probes because real traffic was too
	// thin; SuccessRate, TopError and ProbeLatencyMs then describe the probes.
	Probed         bool   `json:"probed,omitempty"`
	ProbeLatencyMs *int64 `json:"probe_latency_ms,omitempty"`
	// Volumes are admin-only so the page cannot be used to size the fleet.
	Requests      int64 `json:"requests,omitempty"`
	Errors        int64 `json:"errors,omitempty"`
	IgnoredErrors int64 `json:"ignored_errors,omitempty"`
	Probes        int64 `json:"probes,omitempty"`
	ProbeErrors   int64 `json:"probe_errors,omitempty"`
}

type ChannelMonitorV3ComponentStatus struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Model       string   `json:"model,omitempty"`
	Models      []string `json:"models,omitempty"`
	Multiplier  *float64 `json:"multiplier,omitempty"`
	// Status is the latest sufficiently sampled slot, while it is still fresh.
	Status string `json:"status"`
	// Availability is the 0–100 share of answered requests (and counted idle
	// probes) over the configured range; nil without either.
	Availability *float64 `json:"availability"`
	Requests     int64    `json:"requests,omitempty"`
	// Probes is the admin-only number of counted idle probes over the range.
	Probes     int64                   `json:"probes,omitempty"`
	LastDataAt *time.Time              `json:"last_data_at"`
	Cells      []*ChannelMonitorV3Cell `json:"cells"`
	GroupID    int64                   `json:"group_id,omitempty"`
}

type ChannelMonitorV3CategoryStatus struct {
	ID           int64                             `json:"id"`
	Name         string                            `json:"name"`
	Description  string                            `json:"description,omitempty"`
	Availability *float64                          `json:"availability"`
	Components   []ChannelMonitorV3ComponentStatus `json:"components"`
}

type ChannelMonitorV3Window struct {
	// Start is the first slot; End is the exclusive end of the last slot.
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Latest   bool      `json:"latest"`
	HasOlder bool      `json:"has_older"`
}

type ChannelMonitorV3Status struct {
	GeneratedAt       time.Time                        `json:"generated_at"`
	IntervalMinutes   int                              `json:"interval_minutes"`
	Cells             int                              `json:"cells"`
	AvailabilityRange string                           `json:"availability_range"`
	AvailabilitySince time.Time                        `json:"availability_since"`
	DownErrorRate     float64                          `json:"down_error_rate"`
	DegradedErrorRate float64                          `json:"degraded_error_rate"`
	DegradedTTFTMs    int                              `json:"degraded_ttft_ms"`
	MinRequests       int                              `json:"min_requests"`
	DataThrough       *time.Time                       `json:"data_through"`
	FooterNote        string                           `json:"footer_note"`
	Window            ChannelMonitorV3Window           `json:"window"`
	Featured          *ChannelMonitorV3ComponentStatus `json:"featured"`
	Categories        []ChannelMonitorV3CategoryStatus `json:"categories"`
	OpenIncidents     int                              `json:"open_incidents"`
	// IdleProbe tells the page that quiet slots of some shown components are
	// filled in by idle probes.
	IdleProbe bool `json:"idle_probe"`
}

// ChannelMonitorV3Incident is a run of consecutive down slots of one component.
type ChannelMonitorV3Incident struct {
	ComponentID   int64      `json:"component_id"`
	ComponentName string     `json:"component_name"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at"`
	DownSlots     int        `json:"down_slots"`
	TopError      string     `json:"top_error,omitempty"`
	Requests      int64      `json:"requests,omitempty"`
	Errors        int64      `json:"errors,omitempty"`
	// Probes counts the idle probes behind down slots that real traffic was too thin to judge.
	Probes int64 `json:"probes,omitempty"`
}

type ChannelMonitorV3IncidentPage struct {
	Items    []ChannelMonitorV3Incident `json:"items"`
	Total    int                        `json:"total"`
	Page     int                        `json:"page"`
	PageSize int                        `json:"page_size"`
	Since    time.Time                  `json:"since"`
}

// ChannelMonitorV3Viewer is the server-derived scope of one request.
type ChannelMonitorV3Viewer struct {
	Admin bool
	// AllowedGroups are the groups the user may bind keys to.
	AllowedGroups map[int64]bool
	// GroupRates are the user's own multipliers, shown instead of the group's.
	GroupRates map[int64]float64
}

// canSee hides components of deleted or disabled groups from users; admins
// still see them, flagged as unavailable on the settings page.
func (v ChannelMonitorV3Viewer) canSee(c ChannelMonitorV3Component) bool {
	if v.Admin {
		return true
	}
	if c.GroupDeleted || (c.GroupStatus != "" && c.GroupStatus != StatusActive) {
		return false
	}
	return c.Visibility == ChannelMonitorV3VisibilityPublic || v.AllowedGroups[c.GroupID]
}

type ChannelMonitorV3Repository interface {
	GetConfig(ctx context.Context) (*ChannelMonitorV3Config, error)
	// UpdateConfig returns nil when expectedVersion no longer matches.
	UpdateConfig(ctx context.Context, cfg ChannelMonitorV3Config, expectedVersion int, updatedBy int64) (*ChannelMonitorV3Config, error)

	ListCategories(ctx context.Context) ([]ChannelMonitorV3Category, error)
	CreateCategory(ctx context.Context, input ChannelMonitorV3CategoryInput) (*ChannelMonitorV3Category, error)
	// UpdateCategory and the other mutators return nil/false when the row is gone.
	UpdateCategory(ctx context.Context, id int64, input ChannelMonitorV3CategoryInput) (*ChannelMonitorV3Category, error)
	DeleteCategory(ctx context.Context, id int64) (bool, error)

	ListComponents(ctx context.Context) ([]ChannelMonitorV3Component, error)
	GetComponent(ctx context.Context, id int64) (*ChannelMonitorV3Component, error)
	CreateComponent(ctx context.Context, input ChannelMonitorV3ComponentInput) (*ChannelMonitorV3Component, error)
	UpdateComponent(ctx context.Context, id int64, input ChannelMonitorV3ComponentInput) (*ChannelMonitorV3Component, error)
	DeleteComponent(ctx context.Context, id int64) (bool, error)
	// Reorder assigns sort_order by position; unknown IDs are ignored.
	Reorder(ctx context.Context, categoryIDs, componentIDs []int64) error

	// SlotFacts sums V2's one-minute facts of the groups into interval slots of
	// [from, to); latency histograms are read only when asked for.
	SlotFacts(ctx context.Context, groupIDs []int64, from, to time.Time, interval time.Duration, withLatency bool) (*ChannelMonitorV3Facts, error)
	// RangeFacts sums V2's hourly rollups of the groups over [from, to), without slots or latency.
	RangeFacts(ctx context.Context, groupIDs []int64, from, to time.Time) (*ChannelMonitorV3Facts, error)
	// DataThrough is how far the passive aggregation has processed; nil before its first run.
	DataThrough(ctx context.Context) (*time.Time, error)

	// UpdateIdleProbe saves the idle probe settings without touching the
	// versioned page config.
	UpdateIdleProbe(ctx context.Context, probe ChannelMonitorV3IdleProbe) error
	// SuccessCounts counts the successful real requests of the groups in
	// [from, to) per group and model, the way V2 counts successes.
	SuccessCounts(ctx context.Context, groupIDs []int64, from, to time.Time) ([]ChannelMonitorV3Fact, error)
	// ClaimProbe reserves the probe of a group and model for one slot; it
	// returns 0 when the slot is already taken.
	ClaimProbe(ctx context.Context, groupID int64, model string, slot time.Time, slotMinutes int) (int64, error)
	FinishProbe(ctx context.Context, result ChannelMonitorV3ProbeResult) error
	// ProbeFacts sums the finished probes of the groups taken in [from, to),
	// binned into interval slots by when they were taken; interval 0 folds
	// them into one total per group and model.
	ProbeFacts(ctx context.Context, groupIDs []int64, from, to time.Time, interval time.Duration) ([]ChannelMonitorV3ProbeFact, error)
	// ProbeStats summarises each group's finished probes started since then.
	ProbeStats(ctx context.Context, since time.Time) ([]ChannelMonitorV3IdleProbeStat, error)
	PruneProbes(ctx context.Context, before time.Time) error
}

type channelMonitorV3Groups interface {
	GetByID(ctx context.Context, id int64) (*Group, error)
}

type ChannelMonitorV3Service struct {
	repo   ChannelMonitorV3Repository
	groups channelMonitorV3Groups
	now    func() time.Time
}

func NewChannelMonitorV3Service(repo ChannelMonitorV3Repository, groups channelMonitorV3Groups) *ChannelMonitorV3Service {
	return &ChannelMonitorV3Service{repo: repo, groups: groups, now: time.Now}
}

func (s *ChannelMonitorV3Service) Settings(ctx context.Context) (*ChannelMonitorV3Settings, error) {
	cfg, err := s.repo.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	categories, err := s.repo.ListCategories(ctx)
	if err != nil {
		return nil, err
	}
	components, err := s.repo.ListComponents(ctx)
	if err != nil {
		return nil, err
	}
	through, err := s.repo.DataThrough(ctx)
	if err != nil {
		return nil, err
	}
	stats, err := s.repo.ProbeStats(ctx, s.now().UTC().Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	if stats == nil {
		stats = []ChannelMonitorV3IdleProbeStat{}
	}
	return &ChannelMonitorV3Settings{Config: *cfg, Categories: categories, Components: components,
		ErrorCategories: ChannelMonitorV2ErrorCategories, DataThrough: through,
		IdleProbe: cfg.IdleProbe, IdleProbeStats: stats}, nil
}

func (s *ChannelMonitorV3Service) UpdateConfig(ctx context.Context, input ChannelMonitorV3Config, updatedBy int64) (*ChannelMonitorV3Config, error) {
	if err := normalizeChannelMonitorV3Config(&input); err != nil {
		return nil, err
	}
	if input.FeaturedComponentID != nil {
		component, err := s.repo.GetComponent(ctx, *input.FeaturedComponentID)
		if err != nil {
			return nil, err
		}
		if component == nil {
			return nil, channelMonitorV3Invalid("featured component %d does not exist", *input.FeaturedComponentID)
		}
	}
	updated, err := s.repo.UpdateConfig(ctx, input, input.Version, updatedBy)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, ErrChannelMonitorV3Conflict
	}
	return updated, nil
}

func normalizeChannelMonitorV3Config(cfg *ChannelMonitorV3Config) error {
	if !slices.Contains(ChannelMonitorV3Intervals, cfg.IntervalMinutes) {
		return channelMonitorV3Invalid("interval must be one of 1, 2, 3, 5, 10, 15, 30 or 60 minutes")
	}
	if cfg.Cells < 30 || cfg.Cells > 120 {
		return channelMonitorV3Invalid("cells must be 30-120")
	}
	if _, ok := channelMonitorV3Ranges[cfg.AvailabilityRange]; !ok {
		return channelMonitorV3Invalid("availability range must be 24h, 7d or 30d")
	}
	if cfg.DownErrorRate <= 0 || cfg.DownErrorRate > 1 || cfg.DegradedErrorRate <= 0 || cfg.DegradedErrorRate > cfg.DownErrorRate {
		return channelMonitorV3Invalid("error rates must satisfy 0 < degraded <= down <= 1")
	}
	if !slices.Contains(ChannelMonitorV3TTFTThresholds, cfg.DegradedTTFTMs) {
		return channelMonitorV3Invalid("first-token threshold must be one of the latency buckets")
	}
	if cfg.MinRequests < 1 || cfg.MinRequests > 1000 {
		return channelMonitorV3Invalid("minimum requests must be 1-1000")
	}
	ignored := make([]string, 0, len(cfg.IgnoredErrorCategories))
	for _, category := range cfg.IgnoredErrorCategories {
		if !slices.Contains(ChannelMonitorV2ErrorCategories, category) {
			return channelMonitorV3Invalid("unknown error category %q", category)
		}
		if !slices.Contains(ignored, category) {
			ignored = append(ignored, category)
		}
	}
	cfg.IgnoredErrorCategories = ignored
	cfg.FooterNote = strings.TrimSpace(cfg.FooterNote)
	if utf8.RuneCountInString(cfg.FooterNote) > 500 {
		return channelMonitorV3Invalid("footer note must be at most 500 characters")
	}
	if cfg.FeaturedComponentID != nil && *cfg.FeaturedComponentID <= 0 {
		cfg.FeaturedComponentID = nil
	}
	return nil
}

func (s *ChannelMonitorV3Service) CreateCategory(ctx context.Context, input ChannelMonitorV3CategoryInput) (*ChannelMonitorV3Category, error) {
	if err := normalizeChannelMonitorV3Category(&input); err != nil {
		return nil, err
	}
	existing, err := s.repo.ListCategories(ctx)
	if err != nil {
		return nil, err
	}
	if len(existing) >= channelMonitorV3MaxCategories {
		return nil, channelMonitorV3Invalid("at most %d categories", channelMonitorV3MaxCategories)
	}
	return s.repo.CreateCategory(ctx, input)
}

func (s *ChannelMonitorV3Service) UpdateCategory(ctx context.Context, id int64, input ChannelMonitorV3CategoryInput) (*ChannelMonitorV3Category, error) {
	if err := normalizeChannelMonitorV3Category(&input); err != nil {
		return nil, err
	}
	updated, err := s.repo.UpdateCategory(ctx, id, input)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, ErrChannelMonitorV3NotFound
	}
	return updated, nil
}

// DeleteCategory keeps its components; they leave the list until moved to
// another category (or stay as the featured card).
func (s *ChannelMonitorV3Service) DeleteCategory(ctx context.Context, id int64) error {
	ok, err := s.repo.DeleteCategory(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrChannelMonitorV3NotFound
	}
	return nil
}

func normalizeChannelMonitorV3Category(input *ChannelMonitorV3CategoryInput) error {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 64 {
		return channelMonitorV3Invalid("category name is required (at most 64 characters)")
	}
	if utf8.RuneCountInString(input.Description) > 500 {
		return channelMonitorV3Invalid("category description must be at most 500 characters")
	}
	return nil
}

func (s *ChannelMonitorV3Service) CreateComponent(ctx context.Context, input ChannelMonitorV3ComponentInput) (*ChannelMonitorV3Component, error) {
	if err := s.validateComponent(ctx, &input); err != nil {
		return nil, err
	}
	existing, err := s.repo.ListComponents(ctx)
	if err != nil {
		return nil, err
	}
	if len(existing) >= channelMonitorV3MaxComponents {
		return nil, channelMonitorV3Invalid("at most %d components", channelMonitorV3MaxComponents)
	}
	return s.repo.CreateComponent(ctx, input)
}

func (s *ChannelMonitorV3Service) UpdateComponent(ctx context.Context, id int64, input ChannelMonitorV3ComponentInput) (*ChannelMonitorV3Component, error) {
	if err := s.validateComponent(ctx, &input); err != nil {
		return nil, err
	}
	updated, err := s.repo.UpdateComponent(ctx, id, input)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, ErrChannelMonitorV3NotFound
	}
	return updated, nil
}

func (s *ChannelMonitorV3Service) DeleteComponent(ctx context.Context, id int64) error {
	ok, err := s.repo.DeleteComponent(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrChannelMonitorV3NotFound
	}
	return nil
}

func (s *ChannelMonitorV3Service) Reorder(ctx context.Context, categoryIDs, componentIDs []int64) error {
	if len(categoryIDs) > channelMonitorV3MaxCategories || len(componentIDs) > channelMonitorV3MaxComponents {
		return channelMonitorV3Invalid("too many items to reorder")
	}
	return s.repo.Reorder(ctx, categoryIDs, componentIDs)
}

func (s *ChannelMonitorV3Service) validateComponent(ctx context.Context, input *ChannelMonitorV3ComponentInput) error {
	if err := normalizeChannelMonitorV3Component(input); err != nil {
		return err
	}
	if input.CategoryID != nil {
		categories, err := s.repo.ListCategories(ctx)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(categories, func(c ChannelMonitorV3Category) bool { return c.ID == *input.CategoryID }) {
			return channelMonitorV3Invalid("category %d does not exist", *input.CategoryID)
		}
	}
	if s.groups != nil {
		if group, err := s.groups.GetByID(ctx, input.GroupID); err != nil || group == nil {
			return channelMonitorV3Invalid("group %d does not exist", input.GroupID)
		}
	}
	return nil
}

func normalizeChannelMonitorV3Component(input *ChannelMonitorV3ComponentInput) error {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Model = strings.TrimSpace(input.Model)
	if input.Models != nil {
		models := make([]string, 0, len(input.Models))
		for _, model := range input.Models {
			model = strings.TrimSpace(model)
			if model == "" || utf8.RuneCountInString(model) > 100 {
				return channelMonitorV3Invalid("each selected model must contain 1 to 100 characters")
			}
			if !slices.Contains(models, model) {
				models = append(models, model)
			}
		}
		input.Models = models
		input.Model = ""
		if len(models) > 0 {
			input.Model = models[0]
		}
	}
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 100 {
		return channelMonitorV3Invalid("component name is required (at most 100 characters)")
	}
	if utf8.RuneCountInString(input.Description) > 500 {
		return channelMonitorV3Invalid("component description must be at most 500 characters")
	}
	if input.GroupID <= 0 {
		return channelMonitorV3Invalid("component group is required")
	}
	if utf8.RuneCountInString(input.Model) > 100 {
		return channelMonitorV3Invalid("model must be at most 100 characters")
	}
	if input.CategoryID != nil && *input.CategoryID <= 0 {
		input.CategoryID = nil
	}
	if input.DegradedTTFTMs != 0 && !slices.Contains(ChannelMonitorV3TTFTThresholds, input.DegradedTTFTMs) {
		return channelMonitorV3Invalid("first-token threshold must be 0 (inherit) or one of the latency buckets")
	}
	switch input.Visibility {
	case "":
		input.Visibility = ChannelMonitorV3VisibilityGroup
	case ChannelMonitorV3VisibilityGroup, ChannelMonitorV3VisibilityPublic:
	default:
		return channelMonitorV3Invalid("visibility must be group or public")
	}
	return nil
}

// visibleComponents returns the enabled components the viewer may see and their groups.
func (s *ChannelMonitorV3Service) visibleComponents(ctx context.Context, viewer ChannelMonitorV3Viewer) ([]ChannelMonitorV3Component, []int64, error) {
	components, err := s.repo.ListComponents(ctx)
	if err != nil {
		return nil, nil, err
	}
	visible := make([]ChannelMonitorV3Component, 0, len(components))
	groups := []int64{}
	for _, component := range components {
		if component.Enabled && viewer.canSee(component) {
			visible = append(visible, component)
			if !slices.Contains(groups, component.GroupID) {
				groups = append(groups, component.GroupID)
			}
		}
	}
	return visible, groups, nil
}

// Status builds the page for one viewer. end selects an older window by its
// last slot; nil or a future time means the latest window.
func (s *ChannelMonitorV3Service) Status(ctx context.Context, viewer ChannelMonitorV3Viewer, end *time.Time) (*ChannelMonitorV3Status, error) {
	cfg, err := s.repo.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	categories, err := s.repo.ListCategories(ctx)
	if err != nil {
		return nil, err
	}
	visible, groupIDs, err := s.visibleComponents(ctx, viewer)
	if err != nil {
		return nil, err
	}
	through, err := s.repo.DataThrough(ctx)
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	interval := time.Duration(cfg.IntervalMinutes) * time.Minute
	current := now.Truncate(interval)
	oldest := now.Add(-ChannelMonitorV3History).Truncate(interval).Add(interval)
	last := current
	if end != nil {
		if requested := end.UTC().Truncate(interval); requested.Before(current) {
			last = requested
		}
	}
	// Never page past the facts V2 still keeps.
	if minLast := oldest.Add(time.Duration(cfg.Cells-1) * interval); last.Before(minLast) {
		last = minLast
	}
	first := last.Add(-time.Duration(cfg.Cells-1) * interval)
	window := ChannelMonitorV3Window{Start: first, End: last.Add(interval), Latest: !last.Before(current), HasOlder: first.After(oldest)}

	rangeFrom := now.Add(-channelMonitorV3Ranges[cfg.AvailabilityRange]).Truncate(time.Hour)
	status := &ChannelMonitorV3Status{
		GeneratedAt: now, IntervalMinutes: cfg.IntervalMinutes, Cells: cfg.Cells,
		AvailabilityRange: cfg.AvailabilityRange, AvailabilitySince: rangeFrom,
		DownErrorRate: cfg.DownErrorRate, DegradedErrorRate: cfg.DegradedErrorRate, DegradedTTFTMs: cfg.DegradedTTFTMs,
		MinRequests: cfg.MinRequests, DataThrough: through, FooterNote: cfg.FooterNote, Window: window,
		Categories: []ChannelMonitorV3CategoryStatus{},
	}
	if len(visible) == 0 {
		return status, nil
	}

	facts, err := s.repo.SlotFacts(ctx, groupIDs, first, window.End, interval, true)
	if err != nil {
		return nil, err
	}
	totals, err := s.repo.RangeFacts(ctx, groupIDs, rangeFrom, now)
	if err != nil {
		return nil, err
	}
	probed := cfg.IdleProbe.probedGroups(groupIDs)
	status.IdleProbe = len(probed) > 0
	if err := s.attachProbes(ctx, facts, probed, first, window.End, interval); err != nil {
		return nil, err
	}
	if err := s.attachProbes(ctx, totals, probed, rangeFrom, now, 0); err != nil {
		return nil, err
	}
	var latestFacts *ChannelMonitorV3Facts
	latestFirst := current.Add(-time.Duration(cfg.Cells-1) * interval)

	byCategory := map[int64][]ChannelMonitorV3ComponentStatus{}
	for _, component := range visible {
		item := s.componentStatus(*cfg, component, facts, totals, first, interval, viewer)
		if cfg.FeaturedComponentID != nil && *cfg.FeaturedComponentID == component.ID {
			featured := item
			if !window.Latest {
				// The featured card always shows the current window.
				if latestFacts == nil {
					if latestFacts, err = s.repo.SlotFacts(ctx, []int64{component.GroupID}, latestFirst, current.Add(interval), interval, true); err != nil {
						return nil, err
					}
					if err := s.attachProbes(ctx, latestFacts, cfg.IdleProbe.probedGroups([]int64{component.GroupID}), latestFirst, current.Add(interval), interval); err != nil {
						return nil, err
					}
				}
				featured = s.componentStatus(*cfg, component, latestFacts, totals, latestFirst, interval, viewer)
			}
			status.Featured = &featured
		}
		if component.CategoryID != nil {
			byCategory[*component.CategoryID] = append(byCategory[*component.CategoryID], item)
		}
		if window.Latest {
			if runs := channelMonitorV3RunsFromCells(component, item.Cells, interval, now, false); len(runs) > 0 && runs[len(runs)-1].EndedAt == nil {
				status.OpenIncidents++
			}
		}
	}
	for _, category := range categories {
		items := byCategory[category.ID]
		if len(items) == 0 {
			continue
		}
		status.Categories = append(status.Categories, ChannelMonitorV3CategoryStatus{
			ID: category.ID, Name: category.Name, Description: category.Description,
			Availability: channelMonitorV3MeanAvailability(items), Components: items,
		})
	}
	return status, nil
}

func (s *ChannelMonitorV3Service) componentStatus(cfg ChannelMonitorV3Config, component ChannelMonitorV3Component, facts, totals *ChannelMonitorV3Facts, first time.Time, interval time.Duration, viewer ChannelMonitorV3Viewer) ChannelMonitorV3ComponentStatus {
	item := ChannelMonitorV3ComponentStatus{
		ID: component.ID, Name: component.Name, Description: component.Description, Model: component.Model, Models: component.Models,
		Status: ChannelMonitorV3StatusUnknown,
		Cells:  channelMonitorV3Cells(cfg, component, facts, first, cfg.Cells, interval, viewer.Admin),
	}
	for i := len(item.Cells) - 1; i >= 0; i-- {
		cell := item.Cells[i]
		if cell == nil {
			continue
		}
		if item.LastDataAt == nil {
			at := cell.Start
			item.LastDataAt = &at
		}
		if cell.Status != ChannelMonitorV3StatusInsufficient {
			// Quiet traffic is unknown, not evidence that an old outage persists.
			// Use the viewed window so historical pages have the same semantics.
			windowEnd := first.Add(time.Duration(cfg.Cells) * interval)
			if now := s.now().UTC(); now.Before(windowEnd) {
				windowEnd = now
			}
			freshness := 15 * time.Minute
			if interval > freshness {
				freshness = interval
			}
			if !cell.Start.Add(interval).Before(windowEnd.Add(-freshness)) {
				item.Status = cell.Status
			}
			break
		}
	}
	total := channelMonitorV3Accumulate(cfg, component, totals, time.Time{}, 0)[0]
	if counted, probes := total.counted(), total.probeCounted(); counted+probes > 0 {
		availability := float64(total.success+total.probeOK) * 100 / float64(counted+probes)
		item.Availability = &availability
		if viewer.Admin {
			item.Requests, item.Probes = counted, probes
		}
	}
	if component.ShowMultiplier {
		rate := component.GroupRateMultiplier
		if custom, ok := viewer.GroupRates[component.GroupID]; ok {
			rate = custom
		}
		item.Multiplier = &rate
	}
	if viewer.Admin {
		item.GroupID = component.GroupID
	}
	return item
}

type channelMonitorV3Acc struct {
	success    int64
	errors     int64
	ignored    int64
	categories map[string]int64
	ttft       map[int64]int64

	probeOK         int64
	probeErrors     int64
	probeCategories map[string]int64
	// probeLatency maps a median probe latency to the number of probes behind it.
	probeLatency map[int64]int64
}

func (a *channelMonitorV3Acc) counted() int64      { return a.success + a.errors }
func (a *channelMonitorV3Acc) probeCounted() int64 { return a.probeOK + a.probeErrors }

// channelMonitorV3Accumulate folds the facts of one component into count
// slots starting at first; count 0 folds everything into a single total.
// Ignored error categories are taken out of the counted errors.
func channelMonitorV3Accumulate(cfg ChannelMonitorV3Config, component ChannelMonitorV3Component, facts *ChannelMonitorV3Facts, first time.Time, count int, interval ...time.Duration) []channelMonitorV3Acc {
	size := count
	if size < 1 {
		size = 1
	}
	accs := make([]channelMonitorV3Acc, size)
	if facts == nil {
		return accs
	}
	index := func(groupID int64, model string, slot time.Time) int {
		if groupID != component.GroupID || !component.includesModel(model) {
			return -1
		}
		if count == 0 {
			return 0
		}
		i := int(slot.Sub(first) / interval[0])
		if i < 0 || i >= count {
			return -1
		}
		return i
	}
	for _, f := range facts.Metrics {
		if i := index(f.GroupID, f.Model, f.Slot); i >= 0 {
			accs[i].success += f.Success
			accs[i].errors += f.Errors
		}
	}
	for _, f := range facts.Errors {
		i := index(f.GroupID, f.Model, f.Slot)
		if i < 0 {
			continue
		}
		if slices.Contains(cfg.IgnoredErrorCategories, f.Category) {
			accs[i].ignored += f.Errors
			continue
		}
		if accs[i].categories == nil {
			accs[i].categories = map[string]int64{}
		}
		accs[i].categories[f.Category] += f.Errors
	}
	for _, f := range facts.Latency {
		if i := index(f.GroupID, f.Model, f.Slot); i >= 0 {
			if accs[i].ttft == nil {
				accs[i].ttft = map[int64]int64{}
			}
			accs[i].ttft[f.UpperBound] += f.Count
		}
	}
	for i := range accs {
		accs[i].errors -= min(accs[i].ignored, accs[i].errors)
	}
	for _, f := range facts.Probes {
		i := index(f.GroupID, f.Model, f.Slot)
		switch {
		case i < 0 || channelMonitorV3ProbeIgnored(f.Category):
		case f.Category == "":
			accs[i].probeOK += f.Count
			if accs[i].probeLatency == nil {
				accs[i].probeLatency = map[int64]int64{}
			}
			accs[i].probeLatency[f.LatencyP50Ms] += f.Count
		default:
			accs[i].probeErrors += f.Count
			if accs[i].probeCategories == nil {
				accs[i].probeCategories = map[string]int64{}
			}
			accs[i].probeCategories[f.Category] += f.Count
		}
	}
	return accs
}

func channelMonitorV3Cells(cfg ChannelMonitorV3Config, component ChannelMonitorV3Component, facts *ChannelMonitorV3Facts, first time.Time, count int, interval time.Duration, admin bool) []*ChannelMonitorV3Cell {
	accs := channelMonitorV3Accumulate(cfg, component, facts, first, count, interval)
	threshold := component.DegradedTTFTMs
	if threshold <= 0 {
		threshold = cfg.DegradedTTFTMs
	}
	cells := make([]*ChannelMonitorV3Cell, count)
	for i, acc := range accs {
		counted, probes := acc.counted(), acc.probeCounted()
		if counted == 0 && probes == 0 {
			continue
		}
		cell := &ChannelMonitorV3Cell{Start: first.Add(time.Duration(i) * interval)}
		if counted < int64(cfg.MinRequests) && probes > 0 {
			// Real traffic is too thin to judge, so the slot's idle probes speak for it.
			cell.Probed = true
			cell.SuccessRate = float64(acc.probeOK) / float64(probes)
			cell.ProbeLatencyMs = channelMonitorV3P50(acc.probeLatency)
			cell.TopError = channelMonitorV3TopError(acc.probeCategories)
			cell.Status = channelMonitorV3Judge(cfg, float64(acc.probeErrors)/float64(probes), cell.ProbeLatencyMs, threshold)
		} else {
			cell.SuccessRate = float64(acc.success) / float64(counted)
			cell.TTFTP50Ms = channelMonitorV3P50(acc.ttft)
			cell.TopError = channelMonitorV3TopError(acc.categories)
			cell.Status = ChannelMonitorV3StatusInsufficient
			if counted >= int64(cfg.MinRequests) {
				cell.Status = channelMonitorV3Judge(cfg, float64(acc.errors)/float64(counted), cell.TTFTP50Ms, threshold)
			}
		}
		if admin {
			cell.Requests, cell.Errors, cell.IgnoredErrors = counted, acc.errors, acc.ignored
			cell.Probes, cell.ProbeErrors = probes, acc.probeErrors
		}
		cells[i] = cell
	}
	return cells
}

// channelMonitorV3Judge colours a slot from its error rate and median latency.
func channelMonitorV3Judge(cfg ChannelMonitorV3Config, errorRate float64, latencyMs *int64, thresholdMs int) string {
	switch {
	case errorRate >= cfg.DownErrorRate:
		return ChannelMonitorV3StatusDown
	case errorRate >= cfg.DegradedErrorRate || (latencyMs != nil && *latencyMs > int64(thresholdMs)):
		return ChannelMonitorV3StatusDegraded
	default:
		return ChannelMonitorV3StatusOperational
	}
}

// channelMonitorV3P50 returns the histogram bucket holding the median, the
// same bucket V2 reports as first-token P50.
func channelMonitorV3P50(hist map[int64]int64) *int64 {
	var total int64
	bounds := make([]int64, 0, len(hist))
	for bound, count := range hist {
		bounds = append(bounds, bound)
		total += count
	}
	if total == 0 {
		return nil
	}
	sort.Slice(bounds, func(i, j int) bool { return bounds[i] < bounds[j] })
	var cumulative int64
	for _, bound := range bounds {
		cumulative += hist[bound]
		if cumulative*2 >= total {
			value := bound
			return &value
		}
	}
	value := bounds[len(bounds)-1]
	return &value
}

func channelMonitorV3TopError(categories map[string]int64) string {
	top, best := "", int64(0)
	for category, count := range categories {
		if count > best || (count == best && category < top) {
			top, best = category, count
		}
	}
	return top
}

// channelMonitorV3MeanAvailability averages the components that have data,
// so a busy component does not outweigh a quiet one in its category.
func channelMonitorV3MeanAvailability(items []ChannelMonitorV3ComponentStatus) *float64 {
	sum, n := 0.0, 0
	for _, item := range items {
		if item.Availability != nil {
			sum += *item.Availability
			n++
		}
	}
	if n == 0 {
		return nil
	}
	mean := sum / float64(n)
	return &mean
}

// Incidents lists outages over the history V2 keeps, ongoing ones first; see
// channelMonitorV3RunsFromCells for how down slots become incidents.
func (s *ChannelMonitorV3Service) Incidents(ctx context.Context, viewer ChannelMonitorV3Viewer, page, pageSize int) (*ChannelMonitorV3IncidentPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > channelMonitorV3IncidentPage {
		pageSize = 20
	}
	cfg, err := s.repo.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	visible, groupIDs, err := s.visibleComponents(ctx, viewer)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	interval := time.Duration(cfg.IntervalMinutes) * time.Minute
	first := now.Add(-ChannelMonitorV3History).Truncate(interval).Add(interval)
	count := int(now.Truncate(interval).Sub(first)/interval) + 1
	result := &ChannelMonitorV3IncidentPage{Items: []ChannelMonitorV3Incident{}, Page: page, PageSize: pageSize, Since: first}
	if len(visible) == 0 {
		return result, nil
	}
	// Down depends on error rates only, so the history skips latency histograms.
	end := first.Add(time.Duration(count) * interval)
	facts, err := s.repo.SlotFacts(ctx, groupIDs, first, end, interval, false)
	if err != nil {
		return nil, err
	}
	if err := s.attachProbes(ctx, facts, cfg.IdleProbe.probedGroups(groupIDs), first, end, interval); err != nil {
		return nil, err
	}
	all := []ChannelMonitorV3Incident{}
	for _, component := range visible {
		all = append(all, channelMonitorV3Runs(*cfg, component, facts, first, count, interval, now, viewer.Admin)...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if (all[i].EndedAt == nil) != (all[j].EndedAt == nil) {
			return all[i].EndedAt == nil
		}
		return all[i].StartedAt.After(all[j].StartedAt)
	})
	result.Total = len(all)
	from := min((page-1)*pageSize, len(all))
	result.Items = all[from:min(from+pageSize, len(all))]
	return result, nil
}

func channelMonitorV3Runs(cfg ChannelMonitorV3Config, component ChannelMonitorV3Component, facts *ChannelMonitorV3Facts, first time.Time, count int, interval time.Duration, now time.Time, admin bool) []ChannelMonitorV3Incident {
	return channelMonitorV3RunsFromCells(component, channelMonitorV3Cells(cfg, component, facts, first, count, interval, true), interval, now, admin)
}

// channelMonitorV3RunsFromCells turns down slots into incidents. Quiet slots
// (too little traffic) prove nothing either way, so down slots separated by at
// most channelMonitorV3IncidentBridge of quiet belong to one incident; a
// healthy slot or a longer silence ends it at the end of its last down slot.
// A run red for less than ChannelMonitorV3IncidentMinDuration is a blip, not
// an incident. A run whose last down slot is that recent is still ongoing.
func channelMonitorV3RunsFromCells(component ChannelMonitorV3Component, cells []*ChannelMonitorV3Cell, interval time.Duration, now time.Time, admin bool) []ChannelMonitorV3Incident {
	runs := []ChannelMonitorV3Incident{}
	var run *ChannelMonitorV3Incident
	var redUntil time.Time // end of the run's last down slot
	categories := map[string]int{}
	closeRun := func(ongoing bool) {
		if run == nil {
			return
		}
		// The newest down slot only counts up to now while it is still filling up.
		redEnd := redUntil
		if redEnd.After(now) {
			redEnd = now
		}
		if redEnd.Sub(run.StartedAt) >= ChannelMonitorV3IncidentMinDuration {
			if !ongoing {
				ended := redUntil
				run.EndedAt = &ended
			}
			run.TopError = channelMonitorV3TopError(channelMonitorV3Counts(categories))
			if !admin {
				run.Requests, run.Errors, run.Probes = 0, 0, 0
			}
			runs = append(runs, *run)
		}
		run = nil
		categories = map[string]int{}
	}
	for _, cell := range cells {
		if cell == nil || cell.Status == ChannelMonitorV3StatusInsufficient {
			continue
		}
		if run != nil && (cell.Status != ChannelMonitorV3StatusDown || cell.Start.Sub(redUntil) > channelMonitorV3IncidentBridge) {
			closeRun(false)
		}
		if cell.Status != ChannelMonitorV3StatusDown {
			continue
		}
		if run == nil {
			run = &ChannelMonitorV3Incident{ComponentID: component.ID, ComponentName: component.Name, StartedAt: cell.Start}
		}
		run.DownSlots++
		redUntil = cell.Start.Add(interval)
		run.Requests += cell.Requests
		run.Errors += cell.Errors
		if cell.Probed {
			run.Probes += cell.Probes
		}
		if cell.TopError != "" {
			categories[cell.TopError]++
		}
	}
	closeRun(now.Sub(redUntil) <= channelMonitorV3IncidentBridge)
	return runs
}

func channelMonitorV3Counts(in map[string]int) map[string]int64 {
	out := make(map[string]int64, len(in))
	for key, value := range in {
		out[key] = int64(value)
	}
	return out
}
