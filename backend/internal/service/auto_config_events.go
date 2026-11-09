package service

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const (
	AutoConfigEventDegradation = "degradation_applied"
	AutoConfigEventSaved       = "config_saved"
	AutoConfigEventInitial     = "initial_applied"
	AutoConfigEventUpgrade     = "concurrency_upgraded"
	AutoConfigEventCooldown    = "failure_cooldown"
)

// Explicit fields keep credentials, raw upstream responses and request bodies out of history.
type AutoConfigEventDetails struct {
	Degradation         *OAuthDegradationState `json:"degradation,omitempty"`
	ModelMapping        map[string]string      `json:"model_mapping,omitempty"`
	Config              *OAuthAutoConfig       `json:"config,omitempty"`
	Priority            int                    `json:"priority"`
	LoadFactor          int                    `json:"load_factor"`
	Concurrency         int                    `json:"concurrency"`
	PreviousConcurrency int                    `json:"previous_concurrency"`
	GroupIDs            []int64                `json:"group_ids,omitempty"`
	CooldownSeconds     int                    `json:"cooldown_seconds"`
}
type AutoConfigEvent struct {
	ID          int64                  `json:"id"`
	AccountID   int64                  `json:"account_id"`
	AccountName string                 `json:"account_name"`
	Platform    string                 `json:"platform"`
	Kind        string                 `json:"kind"`
	Details     AutoConfigEventDetails `json:"details"`
	CreatedAt   time.Time              `json:"created_at"`
}
type AutoConfigEventRepository interface {
	ListAutoConfigEvents(context.Context, int64, string, int) ([]AutoConfigEvent, error)
}
type autoConfigInitialRecorder interface {
	RecordAutoConfigInitial(context.Context, *Account, []int64) error
}

func ValidAutoConfigEventKind(kind string) bool {
	return kind == "" || kind == AutoConfigEventDegradation || kind == AutoConfigEventSaved || kind == AutoConfigEventInitial || kind == AutoConfigEventUpgrade || kind == AutoConfigEventCooldown
}
func (s *AccountOpsService) ListAutoConfigEvents(ctx context.Context, before int64, kind string, limit int) ([]AutoConfigEvent, error) {
	repo, ok := s.repo.(AutoConfigEventRepository)
	if !ok {
		return nil, errors.New("automatic configuration history unavailable")
	}
	return repo.ListAutoConfigEvents(ctx, before, kind, limit)
}

// Called only after a newly configured account and its group bindings succeeded.
// Logging must not turn a completed import into a failed, retryable creation.
func recordAutoConfigInitial(ctx context.Context, repo AccountRepository, account *Account, groups []int64) {
	if _, applied := account.Extra["auto_config_initial_revision"].(string); !applied {
		return
	}
	if recorder, ok := repo.(autoConfigInitialRecorder); ok {
		if err := recorder.RecordAutoConfigInitial(ctx, account, groups); err != nil {
			slog.Warn("auto_config_initial_history_failed", "account_id", account.ID)
		}
	}
}
