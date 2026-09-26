package service

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

var ErrPelicanShowcaseItemNotFound = infraerrors.NotFound("PELICAN_SHOWCASE_ITEM_NOT_FOUND", "showcase item not found")

// PelicanShowcaseItem is one gallery snapshot. It deliberately carries no account
// identity: users only see the group, model, reasoning effort and timing.
type PelicanShowcaseItem struct {
	ID              int64     `json:"id"`
	GroupID         int64     `json:"group_id"`
	ModelID         string    `json:"model_id"`
	ReasoningEffort string    `json:"reasoning_effort"`
	LatencyMs       int64     `json:"latency_ms"`
	GeneratedAt     time.Time `json:"generated_at"`
	// ResponseText is the raw model output; only the single-item read fills it.
	ResponseText string `json:"response_text,omitempty"`
}

type PelicanShowcaseGroup struct {
	ID       int64                  `json:"id"`
	Name     string                 `json:"name"`
	Platform string                 `json:"platform"`
	Items    []*PelicanShowcaseItem `json:"items"`
}

// PelicanShowcaseView is the user gallery. RetentionDays is 0 when auto cleanup is off.
type PelicanShowcaseView struct {
	Enabled       bool                    `json:"enabled"`
	MaxItems      int                     `json:"max_items"`
	RetentionDays int                     `json:"retention_days"`
	Groups        []*PelicanShowcaseGroup `json:"groups"`
}

// PelicanShowcaseRepository stores gallery snapshots. A zero since/before time means
// "no age limit".
type PelicanShowcaseRepository interface {
	// Publish copies the result into every listed group its plan's account belongs to,
	// then trims those groups to maxItems.
	Publish(ctx context.Context, result *ScheduledTestResult, groupIDs []int64, maxItems int) error
	// Prune deletes snapshots of unlisted groups, beyond maxItems per group, or generated before `before`.
	Prune(ctx context.Context, groupIDs []int64, maxItems int, before time.Time) error
	// ListGroups returns the listed groups that still exist and are active, in display order.
	ListGroups(ctx context.Context, groupIDs []int64) ([]*PelicanShowcaseGroup, error)
	// ListItems returns up to maxItems newest snapshots per group, without the HTML.
	ListItems(ctx context.Context, groupIDs []int64, maxItems int, since time.Time) ([]*PelicanShowcaseItem, error)
	// GetItem returns one snapshot with its HTML if it is still within the gallery limits; nil otherwise.
	GetItem(ctx context.Context, id int64, groupIDs []int64, maxItems int, since time.Time) (*PelicanShowcaseItem, error)
	Delete(ctx context.Context, id int64) (bool, error)
}

type pelicanShowcaseSettings interface {
	GetPelicanShowcaseRuntime(ctx context.Context) (PelicanShowcaseRuntime, error)
}

// PelicanShowcaseService publishes scheduled Pelican HTML results to the user gallery
// and enforces its limits. The gallery keeps copies, so admin history cleanup (per plan
// and 7 days) does not empty it; the gallery has its own count and age limits instead.
type PelicanShowcaseService struct {
	repo     PelicanShowcaseRepository
	settings pelicanShowcaseSettings
}

func NewPelicanShowcaseService(repo PelicanShowcaseRepository, settingService *SettingService) *PelicanShowcaseService {
	return &PelicanShowcaseService{repo: repo, settings: settingService}
}

// PublishScheduledResult copies a successful scheduled Pelican result into the gallery.
// Only drawing questions with real HTML/SVG output qualify: answer-only kinds (candy and
// graded quality questions) share the plan type. Errors are logged, never returned, so
// the admin history write is unaffected.
func (s *PelicanShowcaseService) PublishScheduledResult(ctx context.Context, result *ScheduledTestResult) {
	if s == nil || result == nil || result.ID <= 0 || result.PelicanConfig == nil || result.Status != "success" {
		return
	}
	if cfg := result.PelicanConfig; cfg.Quality != nil || cfg.QuestionKind == "candy" || isBuiltinCandyPlan(cfg) || isOpenAICodexStateProbePlan(cfg) {
		return
	}
	if !pelicanHTMLPattern.MatchString(result.ResponseText) {
		return
	}
	runtime, err := s.settings.GetPelicanShowcaseRuntime(ctx)
	if err != nil {
		logger.LegacyPrintf("service.pelican_showcase", "publish result=%d skipped: %v", result.ID, err)
		return
	}
	if !runtime.Enabled || len(runtime.Config.GroupIDs) == 0 {
		return
	}
	if err := s.repo.Publish(ctx, result, runtime.Config.GroupIDs, runtime.Config.MaxItems); err != nil {
		logger.LegacyPrintf("service.pelican_showcase", "publish result=%d failed: %v", result.ID, err)
	}
}

// Cleanup enforces the gallery limits: groups removed from the selection, snapshots
// beyond the per-group count and, with auto cleanup on, snapshots past the retention
// window. It also runs while the gallery is switched off, like paused-plan history
// cleanup. Unreadable settings skip the round, so a bad config never deletes snapshots.
func (s *PelicanShowcaseService) Cleanup(ctx context.Context, now time.Time) {
	if s == nil {
		return
	}
	runtime, err := s.settings.GetPelicanShowcaseRuntime(ctx)
	if err != nil {
		logger.LegacyPrintf("service.pelican_showcase", "cleanup skipped: %v", err)
		return
	}
	cfg := runtime.Config
	if err := s.repo.Prune(ctx, cfg.GroupIDs, cfg.MaxItems, cfg.retentionCutoff(now)); err != nil {
		logger.LegacyPrintf("service.pelican_showcase", "cleanup failed: %v", err)
	}
}

// View returns the gallery without HTML bodies. Groups without snapshots are kept so
// users see which groups are showcased. Limits apply at read time as well, so a lowered
// limit or a removed group is hidden before the next cleanup round deletes it.
func (s *PelicanShowcaseService) View(ctx context.Context, now time.Time) (*PelicanShowcaseView, error) {
	runtime, err := s.settings.GetPelicanShowcaseRuntime(ctx)
	if err != nil {
		return nil, err
	}
	cfg := runtime.Config
	view := &PelicanShowcaseView{Enabled: runtime.Enabled, MaxItems: cfg.MaxItems, Groups: []*PelicanShowcaseGroup{}}
	if cfg.AutoCleanup {
		view.RetentionDays = cfg.RetentionDays
	}
	if !runtime.Enabled || len(cfg.GroupIDs) == 0 {
		return view, nil
	}
	groups, err := s.repo.ListGroups(ctx, cfg.GroupIDs)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return view, nil
	}
	groupIDs := make([]int64, 0, len(groups))
	byID := make(map[int64]*PelicanShowcaseGroup, len(groups))
	for _, group := range groups {
		group.Items = []*PelicanShowcaseItem{}
		groupIDs = append(groupIDs, group.ID)
		byID[group.ID] = group
	}
	items, err := s.repo.ListItems(ctx, groupIDs, cfg.MaxItems, cfg.retentionCutoff(now))
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if group := byID[item.GroupID]; group != nil {
			group.Items = append(group.Items, item)
		}
	}
	view.Groups = groups
	return view, nil
}

// Item returns one snapshot with its HTML, only while the gallery would still show it.
func (s *PelicanShowcaseService) Item(ctx context.Context, id int64, now time.Time) (*PelicanShowcaseItem, error) {
	runtime, err := s.settings.GetPelicanShowcaseRuntime(ctx)
	if err != nil {
		return nil, err
	}
	cfg := runtime.Config
	if !runtime.Enabled || len(cfg.GroupIDs) == 0 || id <= 0 {
		return nil, ErrPelicanShowcaseItemNotFound
	}
	item, err := s.repo.GetItem(ctx, id, cfg.GroupIDs, cfg.MaxItems, cfg.retentionCutoff(now))
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, ErrPelicanShowcaseItemNotFound
	}
	return item, nil
}

// Remove deletes one snapshot, letting admins take an unwanted result off the gallery.
func (s *PelicanShowcaseService) Remove(ctx context.Context, id int64) error {
	deleted, err := s.repo.Delete(ctx, id)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrPelicanShowcaseItemNotFound
	}
	return nil
}
