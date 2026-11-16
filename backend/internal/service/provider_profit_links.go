package service

import (
	"context"
	"math"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrProviderProfitLinkValidation  = infraerrors.BadRequest("PROVIDER_PROFIT_LINK_VALIDATION", "Invalid provider, channel, pagination or confirmation")
	ErrProviderProfitLinkConflict    = infraerrors.Conflict("PROVIDER_PROFIT_LINK_CONFLICT", "The link changed or the channel belongs to another provider; reload before saving")
	ErrProviderProfitLinkNotFound    = infraerrors.NotFound("PROVIDER_PROFIT_LINK_NOT_FOUND", "Provider or channel not found")
	ErrProviderProfitLinkUnavailable = infraerrors.ServiceUnavailable("PROVIDER_PROFIT_LINK_UNAVAILABLE", "Provider profit links unavailable")
)

// This projection deliberately excludes connection details, credentials and raw
// observations. Null spend is unknown; it is not a zero or verified event cost.
type ProviderProfitLink struct {
	ProviderID   int64                 `json:"provider_id"`
	ProviderName string                `json:"provider_name"`
	SiteType     string                `json:"site_type"`
	ChannelID    int64                 `json:"channel_id"`
	ChannelName  string                `json:"channel_name"`
	Revision     int64                 `json:"revision"` // Link revision, not the provider refresh counter.
	ObservedAt   *time.Time            `json:"observed_at"`
	Status       string                `json:"status"`
	Metrics      ProviderProfitMetrics `json:"metrics"`
}

type ProviderProfitMetrics struct {
	TodaySpend *float64 `json:"today_spend"`
	TotalSpend *float64 `json:"total_spend"`
	Currency   string   `json:"currency"`
}

type ProviderProfitLinkPage struct {
	Items    []ProviderProfitLink `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
}

type ProviderProfitLinkMutation struct {
	ChannelID int64 `json:"channel_id"`
	Revision  int64 `json:"revision"`
	Confirm   bool  `json:"confirm"`
}

func (m ProviderProfitLinkMutation) Validate(providerID int64) error {
	if providerID <= 0 || m.ChannelID <= 0 || m.Revision <= 0 || m.Revision == math.MaxInt64 || !m.Confirm {
		return ErrProviderProfitLinkValidation
	}
	return nil
}

func ValidateProviderProfitLinkPage(page, size int) error {
	if page < 1 || page > 1000000 || size < 1 || size > 100 {
		return ErrProviderProfitLinkValidation
	}
	return nil
}

type ProviderProfitLinkReader interface {
	ListProviderProfitLinks(context.Context, int, int) (*ProviderProfitLinkPage, error)
}

type ProviderProfitLinkRepository interface {
	ProviderProfitLinkReader
	SetProviderProfitLink(context.Context, int64, ProviderProfitLinkMutation) (*ProviderProfitLink, error)
}

func (s *ChannelService) ProviderProfitLinkRepository() (ProviderProfitLinkRepository, error) {
	if s != nil {
		if r, ok := s.repo.(ProviderProfitLinkRepository); ok {
			return r, nil
		}
	}
	return nil, ErrProviderProfitLinkUnavailable
}
