package service

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Internal collection hint for bounded report caching; never accepted from HTTP.
type profitCollectionKey struct{}

func WithProfitRowCollection(ctx context.Context) context.Context {
	return context.WithValue(ctx, profitCollectionKey{}, true)
}
func CollectProfitRows(ctx context.Context) bool {
	value, _ := ctx.Value(profitCollectionKey{}).(bool)
	return value
}

// ProfitWalletTodaySkew is how far the profit window's end may sit behind
// "now" and still count as today's consumption snapshot. The today preset
// sends the browser's current time, which is slightly earlier than the server.
const ProfitWalletTodaySkew = 5 * time.Minute

// ProfitSpendFreshness is how recently a window snapshot must have been
// observed before a profit read skips another upstream probe for that same
// [from, until). A manual refresh ignores it.
const ProfitSpendFreshness = 2 * time.Minute

const profitOperationsQueryTimeout = 5 * time.Minute

type ProfitOperationsSnapshot struct {
	UpstreamOperationsSnapshot
	Currency string `json:"currency"`
}

type ProfitOperationsRepository interface {
	ReadProfitOperations(context.Context, UpstreamOperationsFilter) (*ProfitOperationsSnapshot, error)
}

func ValidateProfitOperationsFilter(f UpstreamOperationsFilter) error {
	if !slices.Contains([]string{"requests", "standard_charge", "request_income", "estimated_cost", "estimated_profit", "margin", "upstream_cost", "gross_profit", "gross_margin", "name"}, f.Sort) {
		return ErrUpstreamOperationsValidation
	}
	if strings.HasPrefix(f.Upstream, "channel:") {
		id, e := strconv.ParseInt(strings.TrimPrefix(f.Upstream, "channel:"), 10, 64)
		if e != nil || id < 1 || f.Upstream != "channel:"+strconv.FormatInt(id, 10) {
			return ErrUpstreamOperationsValidation
		}
		f.Upstream = ""
	}
	f.Sort = "requests"
	// A complete local calendar month can exceed 31*24h when daylight saving ends.
	return validateUpstreamOperationsFilter(f, 32*24*time.Hour)
}

func NormalizeProfitOperationsFilter(f *UpstreamOperationsFilter) error {
	if f == nil {
		return ErrUpstreamOperationsValidation
	}
	if strings.HasPrefix(f.Upstream, "channel:") {
		id, err := strconv.ParseInt(strings.TrimPrefix(f.Upstream, "channel:"), 10, 64)
		if err != nil || id < 1 || f.Upstream != "channel:"+strconv.FormatInt(id, 10) || (f.ChannelID != 0 && f.ChannelID != id) {
			return ErrUpstreamOperationsValidation
		}
		f.ChannelID, f.Upstream = id, ""
	}
	return ValidateProfitOperationsFilter(*f)
}

// ProfitConsumptionLocation is the calendar day used for 渠道运维「今日消费」.
// New API asks the upstream for Asia/Shanghai midnight through the probe time.
// Sub2API reads the upstream dashboard field today_actual_cost (else today_cost),
// which that site sums from its own process timezone's start of today. The
// snapshot does not store that timezone; the upstream default is Asia/Shanghai.
// The profit report therefore treats both as the current Asia/Shanghai day.
func ProfitConsumptionLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("Asia/Shanghai", 8*3600)
	}
	return loc
}

// ProfitConsumptionDay is [start, end) of the consumption day containing now.
func ProfitConsumptionDay(now time.Time) (start, end time.Time) {
	loc := ProfitConsumptionLocation()
	local := now.In(loc)
	start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return start, start.AddDate(0, 0, 1)
}

// ProfitWindowUsesWalletToday is true when [from, until) is that same consumption
// day: it starts at midnight, and it either ends at the next midnight or is
// "today so far" (ending no earlier than now minus ProfitWalletTodaySkew, and
// not after the next midnight). A morning slice, yesterday, or a multi-day
// window is not that day.
func ProfitWindowUsesWalletToday(from, until, now time.Time) bool {
	start, end := ProfitConsumptionDay(now)
	if !from.Equal(start) || !until.After(start) || until.After(end) {
		return false
	}
	if until.Equal(end) {
		return true
	}
	return !until.Before(now.Add(-ProfitWalletTodaySkew))
}

// ProfitSnapshotDates maps a half-open window onto the inclusive Asia/Shanghai
// calendar dates accepted by Sub2API's dated usage API. end_date is the
// calendar date of the last instant inside the window. The upstream API then
// treats that date as a whole local day, so a partial day is not narrower
// than the calendar day that contains it.
func ProfitSnapshotDates(from, until time.Time) (startDate, endDate string, ok bool) {
	if !until.After(from) {
		return "", "", false
	}
	loc := ProfitConsumptionLocation()
	start := from.In(loc)
	last := until.Add(-time.Nanosecond).In(loc)
	if last.Before(start) {
		return "", "", false
	}
	return start.Format(time.DateOnly), last.Format(time.DateOnly), true
}

// ProviderSpendTarget is one linked provider whose wallet can be probed.
type ProviderSpendTarget struct {
	ID       int64
	Revision int64
}

// ProviderSpendSnapshot is one successful window reading. Amount may include
// spend that did not come from this site.
type ProviderSpendSnapshot struct {
	ProviderID       int64
	From, Until      time.Time
	Amount           *float64
	Currency, Source string
}

// ProviderSpendStore persists window spend separately from today's card.
type ProviderSpendStore interface {
	ListProviderSpendTargets(context.Context) ([]ProviderSpendTarget, error)
	ProviderSpendFresh(context.Context, int64, time.Time, time.Time, time.Duration) (bool, error)
	SaveProviderSpend(context.Context, ProviderSpendSnapshot) error
	RecordProviderSpendError(context.Context, int64, time.Time, time.Time, string) error
}

func (s *ChannelService) ProfitOperations(ctx context.Context, f UpstreamOperationsFilter) (*ProfitOperationsSnapshot, error) {
	if err := NormalizeProfitOperationsFilter(&f); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, profitOperationsQueryTimeout)
	defer cancel()
	repo, ok := s.repo.(ProfitOperationsRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("PROFIT_OPERATIONS_UNAVAILABLE", "Profit operations data source unavailable")
	}
	out, err := repo.ReadProfitOperations(ctx, f)
	// Database drivers can return their own cancellation error. Preserve the
	// request's deadline/cancellation so callers can distinguish a timeout.
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, err
}

// OperationsCostMultiplier distinguishes the scheduling default from a saved
// estimate, and corrupt legacy metadata from an explicit zero cost.
func OperationsCostMultiplier(a *Account) (*float64, string) {
	if a == nil {
		return nil, "account_missing"
	}
	raw, present := a.Extra[AccountCostMultiplierExtraKey]
	if !present || raw == nil {
		v := DefaultAccountCostMultiplier
		return &v, "current_default_0.1"
	}
	v, ok := accountCostMultiplierNumber(raw)
	if !ok {
		return nil, "invalid_multiplier"
	}
	return &v, "current_saved_multiplier"
}
