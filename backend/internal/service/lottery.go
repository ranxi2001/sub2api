package service

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

const lotteryScale = int64(100000000)
const lotteryMaxAmount = int64(1000000000) * lotteryScale

type LotteryService struct {
	client        *dbent.Client
	users         UserRepository
	subscriptions *SubscriptionService
	billingCache  *BillingCacheService
	authCache     APIKeyAuthCacheInvalidator
	settings      *SettingService
}

func NewLotteryService(client *dbent.Client, users UserRepository, subscriptions *SubscriptionService, billingCache *BillingCacheService, authCache APIKeyAuthCacheInvalidator, settings *SettingService) *LotteryService {
	return &LotteryService{client: client, users: users, subscriptions: subscriptions, billingCache: billingCache, authCache: authCache, settings: settings}
}

type LotteryPoolInput struct {
	Name         string  `json:"name"`
	Mode         string  `json:"mode"`
	Threshold    float64 `json:"threshold"`
	GrantEnabled bool    `json:"grant_enabled"`
	DrawEnabled  bool    `json:"draw_enabled"`
	SortOrder    int     `json:"sort_order"`
}

type LotteryPrizeInput struct {
	UpdatedAt time.Time `json:"updated_at"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Weight    int64     `json:"weight"`
	Stock     int64     `json:"stock"`
	Enabled   bool      `json:"enabled"`
	SortOrder int       `json:"sort_order"`
	Amount    float64   `json:"amount"`
	PlanID    int64     `json:"plan_id"`
	Text      string    `json:"text"`
}

type LotteryPrizeView struct {
	UpdatedAt    time.Time `json:"updated_at"`
	ID           int64     `json:"id"`
	PoolID       int64     `json:"pool_id"`
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	Weight       int64     `json:"weight"`
	Stock        int64     `json:"stock"`
	Enabled      bool      `json:"enabled"`
	Available    bool      `json:"available"`
	SortOrder    int       `json:"sort_order"`
	Amount       float64   `json:"amount"`
	PlanID       int64     `json:"plan_id"`
	PlanName     string    `json:"plan_name"`
	GroupID      int64     `json:"group_id"`
	ValidityDays int       `json:"validity_days"`
	Text         string    `json:"text,omitempty"`
	Probability  float64   `json:"probability"`
}

type LotteryPoolView struct {
	ID           int64              `json:"id"`
	Name         string             `json:"name"`
	Mode         string             `json:"mode"`
	Threshold    float64            `json:"threshold"`
	GrantEnabled bool               `json:"grant_enabled"`
	DrawEnabled  bool               `json:"draw_enabled"`
	RuleLocked   bool               `json:"rule_locked"`
	SortOrder    int                `json:"sort_order"`
	Available    int64              `json:"available"`
	Debt         int64              `json:"debt"`
	Progress     float64            `json:"progress"`
	Prizes       []LotteryPrizeView `json:"prizes"`
}

type LotteryDrawView struct {
	ID        int64          `json:"id"`
	PoolID    int64          `json:"pool_id"`
	UserID    int64          `json:"user_id"`
	Prize     map[string]any `json:"prize"`
	CreatedAt time.Time      `json:"created_at"`
}

type LotteryLedgerView struct {
	ID          int64     `json:"id"`
	PoolID      int64     `json:"pool_id"`
	UserID      int64     `json:"user_id"`
	Action      string    `json:"action"`
	SourceType  string    `json:"source_type"`
	SourceID    string    `json:"source_id"`
	Amount      float64   `json:"amount"`
	TicketDelta int64     `json:"ticket_delta"`
	CreatedAt   time.Time `json:"created_at"`
}

func lotteryUnits(amount float64) (int64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 || amount > 1e9 {
		return 0, infraerrors.BadRequest("LOTTERY_INVALID_AMOUNT", "amount must be between 0 and 1,000,000,000 USD")
	}
	d := decimal.NewFromFloat(amount)
	if !d.Equal(d.Round(8)) {
		return 0, infraerrors.BadRequest("LOTTERY_INVALID_AMOUNT", "amount supports at most eight decimal places")
	}
	return d.Shift(8).IntPart(), nil
}

func lotteryAmount(units int64) float64 { return decimal.NewFromInt(units).Shift(-8).InexactFloat64() }

func lotteryName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", infraerrors.BadRequest("LOTTERY_INVALID_NAME", "name must contain 1 to 100 characters")
	}
	return name, nil
}

func lotteryPoolView(p *dbent.LotteryPool, a *dbent.LotteryAccount, prizes []LotteryPrizeView) LotteryPoolView {
	v := LotteryPoolView{ID: p.ID, Name: p.Name, Mode: p.Mode, Threshold: lotteryAmount(p.ThresholdUnits), GrantEnabled: p.GrantEnabled, DrawEnabled: p.DrawEnabled, RuleLocked: p.RuleLocked, SortOrder: p.SortOrder, Prizes: prizes}
	if a != nil {
		if a.Earned >= a.Used {
			v.Available = a.Earned - a.Used
		} else {
			v.Debt = a.Used - a.Earned
		}
		if p.Mode == "cumulative" {
			v.Progress = lotteryAmount(a.TotalUnits % p.ThresholdUnits)
		}
	}
	return v
}

func lotteryPrizeView(p *dbent.LotteryPrize) LotteryPrizeView {
	return LotteryPrizeView{UpdatedAt: p.UpdatedAt, ID: p.ID, PoolID: p.PoolID, Name: p.Name, Kind: p.Kind, Weight: p.Weight, Stock: p.Stock, Enabled: p.Enabled, Available: p.Enabled && p.Stock != 0, SortOrder: p.SortOrder, Amount: lotteryAmount(p.AmountUnits), PlanID: p.PlanID, PlanName: p.PlanName, GroupID: p.GroupID, ValidityDays: p.ValidityDays, Text: p.Text}
}

func lotteryDrawView(d *dbent.LotteryDraw) *LotteryDrawView {
	return &LotteryDrawView{ID: d.ID, PoolID: d.PoolID, UserID: d.UserID, Prize: d.PrizeSnapshot, CreatedAt: d.CreatedAt}
}

func lotteryTicketDelta(mode string, threshold, totalBefore, netBefore, netAfter int64) (int64, error) {
	if threshold <= 0 || totalBefore < netBefore || netAfter < 0 || netAfter > math.MaxInt64-(totalBefore-netBefore) {
		return 0, fmt.Errorf("invalid lottery accounting state")
	}
	if mode == "cumulative" {
		return (totalBefore-netBefore+netAfter)/threshold - totalBefore/threshold, nil
	}
	var before, after int64
	if netBefore >= threshold {
		before = 1
	}
	if netAfter >= threshold {
		after = 1
	}
	return after - before, nil
}
