package service

// Provider catalog semantics follow mizaawa/zayuapi v0.2.219, commit
// a71aa9615ccb55a19973908c9f261b5ba25a48c6 (LGPL-3.0). This fork uses a
// separate relational catalog and optimistic revisions, not whole-setting writes.

import (
	"context"
	"math"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrMatureProviderValidation = infraerrors.BadRequest("PROVIDER_VALIDATION", "Invalid provider configuration")
	ErrMatureProviderConflict   = infraerrors.Conflict("PROVIDER_REVISION", "Provider changed; reload before saving")
	ErrMatureProviderNotFound   = infraerrors.NotFound("PROVIDER_NOT_FOUND", "Provider not found")
	ErrMatureProviderKeyPending = infraerrors.Conflict("PROVIDER_KEY_CREATE_PENDING", "The previous key creation is pending or unconfirmed; inspect upstream keys before creating another")
)

type ProviderMetrics struct {
	Balance       *float64 `json:"balance"`
	TodaySpend    *float64 `json:"today_spend"`
	TotalSpend    *float64 `json:"total_spend"`
	TotalRecharge *float64 `json:"total_recharge"`
	Currency      string   `json:"currency"`
}

// Provider records do not participate in routing, billing, or channel ownership.
type MatureProvider struct {
	RefreshIntervalSeconds  int             `json:"refresh_interval_seconds"`
	AuthMode                string          `json:"auth_mode"`
	Account                 string          `json:"account"`
	UserID                  string          `json:"user_id"`
	HasCredential           bool            `json:"has_credential"`
	LastError               string          `json:"last_error"`
	ID                      int64           `json:"id"`
	Revision                int64           `json:"revision"`
	Name                    string          `json:"name"`
	SiteType                string          `json:"site_type"`
	BaseURL                 string          `json:"base_url"`
	Notes                   string          `json:"notes"`
	CostMultiplier          *float64        `json:"cost_multiplier"`
	LowBalanceThreshold     *float64        `json:"low_balance_threshold"`
	SuppressLowBalanceAlert bool            `json:"suppress_low_balance_alert"`
	Status                  string          `json:"status"`
	Metrics                 ProviderMetrics `json:"metrics"`
	ObservedAt              *time.Time      `json:"observed_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

type MatureProviderInput struct {
	RefreshIntervalSeconds  *int     `json:"refresh_interval_seconds"`
	AuthMode                string   `json:"auth_mode"`
	Account                 string   `json:"account"`
	UserID                  string   `json:"user_id"`
	Password                string   `json:"password"`
	AccessToken             string   `json:"access_token"`
	RefreshToken            string   `json:"refresh_token"`
	TokenType               string   `json:"token_type"`
	CredentialCipher        *string  `json:"-"`
	Name                    string   `json:"name"`
	SiteType                string   `json:"site_type"`
	BaseURL                 string   `json:"base_url"`
	Notes                   string   `json:"notes"`
	CostMultiplier          *float64 `json:"cost_multiplier"`
	LowBalanceThreshold     *float64 `json:"low_balance_threshold"`
	SuppressLowBalanceAlert bool     `json:"suppress_low_balance_alert"`
	Revision                *int64   `json:"revision"`
	Confirm                 bool     `json:"confirm"`
}

type MatureProviderPage struct {
	Items    []MatureProvider `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}

type MatureProviderRepository interface {
	ListMatureProviders(context.Context, string, string, int, int) (MatureProviderPage, error)
	GetMatureProvider(context.Context, int64) (*MatureProvider, error)
	CreateMatureProvider(context.Context, MatureProviderInput) (*MatureProvider, error)
	UpdateMatureProvider(context.Context, int64, MatureProviderInput) (*MatureProvider, error)
	DeleteMatureProvider(context.Context, int64, int64) error
	WithMatureProviderSession(context.Context, int64, int64, func(context.Context, *MatureProvider, string) (ProviderSessionUpdate, error)) (*MatureProvider, error)
	ReserveMatureProviderKey(context.Context, int64, string, string) (bool, int, []byte, error)
	CompleteMatureProviderKey(context.Context, int64, string, string, int, []byte) error
	ClaimMatureProviderRefresh(context.Context) (int64, int64, error)
}

type ProviderSessionUpdate struct {
	Cipher    *string
	Metrics   *ProviderMetrics
	Status    string
	LastError string
}

func (s *ChannelService) MatureProviderRepository() (MatureProviderRepository, error) {
	if s != nil {
		if r, ok := s.repo.(MatureProviderRepository); ok {
			return r, nil
		}
	}
	return nil, infraerrors.ServiceUnavailable("PROVIDER_UNAVAILABLE", "Provider catalog unavailable")
}

// This is lexical validation only: storing a record must not resolve DNS or
// contact an upstream. Every future network request must validate at dial time.
func ValidateMatureProviderInput(in *MatureProviderInput) error {
	if in.RefreshIntervalSeconds != nil && *in.RefreshIntervalSeconds != 0 && (*in.RefreshIntervalSeconds < 30 || *in.RefreshIntervalSeconds > 86400) {
		return ErrMatureProviderValidation
	}
	if in.AuthMode == "" {
		in.AuthMode = "token"
	}
	if in.AuthMode != "token" && in.AuthMode != "password" {
		return ErrMatureProviderValidation
	}
	in.Account, in.UserID = strings.TrimSpace(in.Account), strings.TrimSpace(in.UserID)
	if len(in.Account) > 320 || len(in.UserID) > 128 || strings.IndexFunc(in.Account+in.UserID, unicode.IsControl) >= 0 || (in.AuthMode == "password" && in.Account == "") {
		return ErrMatureProviderValidation
	}
	in.Name, in.Notes = strings.TrimSpace(in.Name), strings.TrimSpace(in.Notes)
	if in.Name == "" || len(in.Name) > 120 || len(in.Notes) > 1000 || strings.IndexFunc(in.Name, unicode.IsControl) >= 0 {
		return ErrMatureProviderValidation
	}
	if in.SiteType != "sub2api" && in.SiteType != "newapi" {
		return ErrMatureProviderValidation
	}
	u, err := url.Parse(strings.TrimSpace(in.BaseURL))
	if err != nil || len(in.BaseURL) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return ErrMatureProviderValidation
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if isBlockedHostname(host) || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return ErrMatureProviderValidation
	}
	if ip := net.ParseIP(host); ip != nil && (isPrivateIP(ip) || !ip.IsGlobalUnicast()) {
		return ErrMatureProviderValidation
	}
	for _, n := range []*float64{in.CostMultiplier, in.LowBalanceThreshold} {
		if n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || *n < 0 || *n > 1e9) {
			return ErrMatureProviderValidation
		}
	}
	in.BaseURL = strings.TrimRight(u.String(), "/")
	return nil
}
