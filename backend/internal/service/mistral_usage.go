package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Mistral 官方用量与套餐查询接口（对齐 mistralai/mistral-vibe：vibe/app_server/_billing.py）：
//
//	GET /v1/billing/budget       -> {vibe_budget:{usage_percentage, initial_budget, currency, reset_at, payg_enabled},
//	                                 api_budget:{usage_percentage, initial_budget, currency, reset_at, payg_enabled}}
//	GET /v1/billing/subscription -> {plan_name, payg_enabled, vibe_payg_enabled, enterprise}
//
// 两个接口均位于官方推理主机 https://api.mistral.ai，直接使用账号 API Key（Bearer）鉴权。
// Vibe Key（前缀 mstrl_）消耗 vibe_budget；常规 La Plateforme API Key 消耗 api_budget。
const (
	mistralAPIHost = "api.mistral.ai"
	mistralAPIBase = "https://" + mistralAPIHost

	mistralQuotaSource     = "mistral_billing_budget"
	mistralQuotaSourcePayg = "mistral_billing_budget_payg"

	mistralExtraSuffixPaygEnabled       = "payg_enabled"
	mistralExtraSuffixVibeUsedPercent   = "vibe_used_percent"
	mistralExtraSuffixVibeInitialBudget = "vibe_initial_budget"
	mistralExtraSuffixAPIUsedPercent    = "api_used_percent"
	mistralExtraSuffixAPIInitialBudget  = "api_initial_budget"
)

var errMistralInvalidResponse = errors.New("invalid mistral billing response")

var mistralPlanNames = map[string]string{
	"FREE":       "Free",
	"INDIVIDUAL": "Pro",
	"EDU":        "Student",
	"TEAM":       "Team",
	"SCALE":      "Scale",
}

// IsMistral 报告账号是否为 Mistral 平台账号。
func (a *Account) IsMistral() bool {
	return a != nil && a.Platform == PlatformMistral
}

// isOfficialMistralHost 报告 URL 是否指向官方 Mistral API 主机。
func isOfficialMistralHost(target string) bool {
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Hostname(), mistralAPIHost)
}

// mistralUsageSupported 报告账号能否查询 Mistral 预算与用量：API Key 账号且推理端点指向官方主机。
func (a *Account) mistralUsageSupported() bool {
	return a.IsMistral() && a.Type == AccountTypeAPIKey && isOfficialMistralHost(a.GetOpenAIBaseURL())
}

type mistralBudgetDetail struct {
	UsagePercentage commandCodeNumber `json:"usage_percentage"`
	InitialBudget   commandCodeNumber `json:"initial_budget"`
	Currency        string            `json:"currency"`
	ResetAt         string            `json:"reset_at"`
	PaygEnabled     *bool             `json:"payg_enabled"`
}

type mistralBudgetPayload struct {
	VibeBudget      *mistralBudgetDetail `json:"vibe_budget"`
	APIBudget       *mistralBudgetDetail `json:"api_budget"`
	UsagePercentage *commandCodeNumber   `json:"usage_percentage"`
	InitialBudget   *commandCodeNumber   `json:"initial_budget"`
	Currency        string               `json:"currency"`
	ResetAt         string               `json:"reset_at"`
	PaygEnabled     *bool                `json:"payg_enabled"`
}

type mistralSubscriptionPayload struct {
	PlanName        string `json:"plan_name"`
	PaygEnabled     *bool  `json:"payg_enabled"`
	VibePaygEnabled *bool  `json:"vibe_payg_enabled"`
}

type mistralUsage struct {
	budget       mistralBudgetPayload
	subscription *mistralSubscriptionPayload
	vibeKey      bool
}

type mistralHTTPError struct {
	status int
	body   string
}

func (e *mistralHTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.status, e.body)
}

func (e *mistralHTTPError) unauthorized() bool {
	// Billing reads require see_org_inference_usage. A 403 can mean that the
	// key still works for inference but cannot read the organization's budget.
	return e.status == http.StatusUnauthorized
}

type mistralUsageClient struct {
	cfg          *config.Config
	httpUpstream HTTPUpstream
	account      *Account
	apiKey       string
	proxyURL     string
	errPrefix    string
}

func newMistralUsageClient(cfg *config.Config, httpUpstream HTTPUpstream, account *Account, proxyURL, errPrefix string) *mistralUsageClient {
	return &mistralUsageClient{
		cfg:          cfg,
		httpUpstream: httpUpstream,
		account:      account,
		apiKey:       strings.TrimSpace(account.GetCNAPIKey()),
		proxyURL:     proxyURL,
		errPrefix:    errPrefix,
	}
}

func (c *mistralUsageClient) get(ctx context.Context, path string, out any) error {
	validatedURL, err := cnValidateProbeURL(c.cfg, mistralAPIBase+path)
	if err != nil {
		return infraerrors.New(http.StatusForbidden, c.errPrefix+"_URL_REJECTED", err.Error())
	}
	callCtx, cancel := context.WithTimeout(ctx, cnQuotaUpstreamTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, validatedURL, nil)
	if err != nil {
		return infraerrors.Newf(http.StatusInternalServerError, c.errPrefix+"_REQUEST_BUILD_FAILED", "build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	c.account.ApplyHeaderOverrides(req.Header)

	resp, err := c.httpUpstream.Do(req, c.proxyURL, c.account.ID, maxInt(c.account.Concurrency, 1))
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, cnQuotaMaxBodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &mistralHTTPError{status: resp.StatusCode, body: truncate(strings.TrimSpace(string(body)), 240)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: %s: %v", errMistralInvalidResponse, path, err)
	}
	return nil
}

func (c *mistralUsageClient) fetch(ctx context.Context, withSubscription bool) (*mistralUsage, error) {
	usage := &mistralUsage{
		vibeKey: strings.HasPrefix(strings.ToLower(c.apiKey), "mstrl_"),
	}
	if err := c.get(ctx, "/v1/billing/budget", &usage.budget); err != nil {
		return nil, err
	}
	active := usage.activeBudget()
	if active == nil {
		return nil, fmt.Errorf("%w: missing budget detail", errMistralInvalidResponse)
	}
	if !withSubscription && active.PaygEnabled != nil {
		return usage, nil
	}
	var subscription mistralSubscriptionPayload
	if err := c.get(ctx, "/v1/billing/subscription", &subscription); err != nil {
		slog.Warn("mistral_subscription_probe_failed", "account_id", c.account.ID, "error", err)
		return usage, nil
	}
	usage.subscription = &subscription
	return usage, nil
}

func (u *mistralUsage) resolvedAPIBudget() *mistralBudgetDetail {
	if u.budget.APIBudget != nil {
		return u.budget.APIBudget
	}
	if u.budget.InitialBudget != nil {
		var used commandCodeNumber
		if u.budget.UsagePercentage != nil {
			used = *u.budget.UsagePercentage
		}
		return &mistralBudgetDetail{
			UsagePercentage: used,
			InitialBudget:   *u.budget.InitialBudget,
			Currency:        u.budget.Currency,
			ResetAt:         u.budget.ResetAt,
			PaygEnabled:     u.budget.PaygEnabled,
		}
	}
	return nil
}

func (u *mistralUsage) activeBudget() *mistralBudgetDetail {
	vibe := u.budget.VibeBudget
	api := u.resolvedAPIBudget()
	if u.vibeKey {
		if vibe != nil {
			return vibe
		}
		return api
	}
	if api != nil {
		return api
	}
	return vibe
}

func (u *mistralUsage) isVibeActive() bool {
	active := u.activeBudget()
	return active != nil && active == u.budget.VibeBudget
}

func (u *mistralUsage) paygEnabled(existingExtra map[string]any) bool {
	active := u.activeBudget()
	if active != nil && active.PaygEnabled != nil {
		return *active.PaygEnabled
	}
	if u.subscription != nil {
		if u.isVibeActive() && u.subscription.VibePaygEnabled != nil {
			return *u.subscription.VibePaygEnabled
		}
		if u.subscription.PaygEnabled != nil {
			return *u.subscription.PaygEnabled
		}
	}
	if existing, ok := existingExtra[cnExtraKey(PlatformMistral, mistralExtraSuffixPaygEnabled)].(bool); ok {
		return existing
	}
	return false
}

func roundMistralPercent(v float64) float64 {
	if v < 0 {
		v = 0
	}
	return math.Round(v*100) / 100
}

func (u *mistralUsage) tiers() []CNQuotaTier {
	active := u.activeBudget()
	if active == nil || active.InitialBudget <= 0 {
		return nil
	}
	return []CNQuotaTier{
		{
			Window:      "monthly",
			UsedPercent: roundMistralPercent(float64(active.UsagePercentage)),
			ResetAt:     cnNormalizeResetTime(active.ResetAt),
		},
	}
}

func (u *mistralUsage) planName() string {
	if u.subscription == nil {
		return ""
	}
	raw := strings.TrimSpace(u.subscription.PlanName)
	if title, ok := mistralPlanNames[strings.ToUpper(raw)]; ok {
		return title
	}
	return raw
}

func (u *mistralUsage) balance(now time.Time, paygEnabled bool) *CNProviderBalanceResult {
	active := u.activeBudget()
	currency := "USD"
	remaining := 0.0
	usedPercent := 0.0
	if active != nil {
		if c := strings.ToUpper(strings.TrimSpace(active.Currency)); c != "" {
			currency = c
		}
		usedPercent = math.Max(0, float64(active.UsagePercentage))
		initial := float64(active.InitialBudget)
		if initial > 0 && usedPercent < 100 {
			remaining = initial * (100 - usedPercent) / 100
			remaining = math.Round(remaining*10000) / 10000
		}
	}
	available := paygEnabled || (active != nil && active.InitialBudget > 0 && usedPercent < 100)
	return &CNProviderBalanceResult{
		Provider:   PlatformMistral,
		Success:    true,
		Balance:    remaining,
		Currency:   currency,
		Balances:   []CNProviderBalanceEntry{{Currency: currency, Balance: remaining}},
		Available:  available,
		StatusCode: http.StatusOK,
		FetchedAt:  now.Unix(),
	}
}

// mistralWindowsBinding 报告账号快照中的月度窗口是否限制调度：开启按量付费（payg_enabled）
// 时允许超出月度预算额度继续请求，不触发阈值停调。
func mistralWindowsBinding(extra map[string]any) bool {
	if len(extra) == 0 {
		return true
	}
	payg, _ := extra[cnExtraKey(PlatformMistral, mistralExtraSuffixPaygEnabled)].(bool)
	return !payg
}

func clearMistralPaygCooldown(ctx context.Context, repo AccountRepository, account *Account, paygEnabled bool, now time.Time) bool {
	if !paygEnabled || account == nil {
		return false
	}
	if account.TempUnschedulableUntil == nil || !now.Before(*account.TempUnschedulableUntil) ||
		!strings.HasPrefix(account.TempUnschedulableReason, cnBalanceLowReasonPrefix) {
		return false
	}
	if err := repo.ClearTempUnschedulable(ctx, account.ID); err != nil {
		slog.Warn("mistral_payg_cooldown_clear_failed", "account_id", account.ID, "error", err)
		return false
	}
	account.TempUnschedulableUntil = nil
	account.TempUnschedulableReason = ""
	return true
}

func mistralUsageExtraUpdates(usage *mistralUsage, tiers []CNQuotaTier, balance *CNProviderBalanceResult, paygEnabled bool, now time.Time) map[string]any {
	updates := cnBalanceExtraUpdates(PlatformMistral, balance, now)
	updates[cnExtraKey(PlatformMistral, cnBalanceExtraSuffixLow)] = !balance.Available
	updates[cnExtraKey(PlatformMistral, mistralExtraSuffixPaygEnabled)] = paygEnabled

	for key, value := range cnQuotaExtraUpdates(PlatformMistral, tiers, now) {
		updates[key] = value
	}
	updates[cnExtraKey(PlatformMistral, cnExtraSuffix5hUsed)] = nil
	updates[cnExtraKey(PlatformMistral, cnExtraSuffix5hReset)] = nil
	updates[cnExtraKey(PlatformMistral, cnExtraSuffixWeeklyUsed)] = nil
	updates[cnExtraKey(PlatformMistral, cnExtraSuffixWeeklyReset)] = nil
	if len(tiers) == 0 {
		updates[cnExtraKey(PlatformMistral, cnExtraSuffixMonthlyUsed)] = nil
		updates[cnExtraKey(PlatformMistral, cnExtraSuffixMonthlyReset)] = nil
	} else if tiers[0].ResetAt == "" {
		updates[cnExtraKey(PlatformMistral, cnExtraSuffixMonthlyReset)] = nil
	}

	if vibe := usage.budget.VibeBudget; vibe != nil && vibe.InitialBudget > 0 {
		updates[cnExtraKey(PlatformMistral, mistralExtraSuffixVibeUsedPercent)] = roundMistralPercent(float64(vibe.UsagePercentage))
		updates[cnExtraKey(PlatformMistral, mistralExtraSuffixVibeInitialBudget)] = float64(vibe.InitialBudget)
	}
	if api := usage.resolvedAPIBudget(); api != nil && api.InitialBudget > 0 {
		updates[cnExtraKey(PlatformMistral, mistralExtraSuffixAPIUsedPercent)] = roundMistralPercent(float64(api.UsagePercentage))
		updates[cnExtraKey(PlatformMistral, mistralExtraSuffixAPIInitialBudget)] = float64(api.InitialBudget)
	}
	return updates
}

func mistralFetchFailure(err error) (status int, message string, ok bool) {
	var httpErr *mistralHTTPError
	switch {
	case errors.As(err, &httpErr) && httpErr.unauthorized():
		return httpErr.status, fmt.Sprintf("Authentication failed (HTTP %d)", httpErr.status), true
	case errors.As(err, &httpErr) && httpErr.status == http.StatusForbidden:
		return httpErr.status, "Billing usage permission denied (HTTP 403); inference credentials may still be valid", true
	case errors.As(err, &httpErr):
		return httpErr.status, fmt.Sprintf("API error (HTTP %d): %s", httpErr.status, httpErr.body), true
	case errors.Is(err, errMistralInvalidResponse):
		return http.StatusOK, "Invalid usage response", true
	default:
		return 0, "", false
	}
}

func (s *CNProviderQuotaService) queryMistralUsage(ctx context.Context, account *Account) (*CNProviderQuotaProbeResult, error) {
	client := newMistralUsageClient(s.cfg, s.httpUpstream, account, s.resolveProxyURL(ctx, account), "CN_QUOTA")
	if client.apiKey == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "CN_QUOTA_NO_APIKEY", "account api_key is empty")
	}
	now := time.Now().UTC()
	result := &CNProviderQuotaProbeResult{
		Provider:  PlatformMistral,
		Source:    mistralQuotaSource,
		FetchedAt: now.Unix(),
	}
	usage, err := client.fetch(ctx, true)
	if err != nil {
		status, message, ok := mistralFetchFailure(err)
		if !ok {
			if isApplicationError(err) {
				return nil, err
			}
			return nil, infraerrors.Newf(http.StatusBadGateway, "CN_QUOTA_REQUEST_FAILED", "%v", err)
		}
		result.StatusCode = status
		result.Error = message
		result.CredentialValid = status != http.StatusUnauthorized
		return result, nil
	}

	paygEnabled := usage.paygEnabled(account.Extra)
	if paygEnabled {
		result.Source = mistralQuotaSourcePayg
	}
	tiers := usage.tiers()
	balance := usage.balance(now, paygEnabled)
	result.StatusCode = http.StatusOK
	result.Tiers = tiers
	result.PlanLevel = usage.planName()
	result.Balance = balance
	result.Success = true
	result.CredentialValid = true

	updates := mistralUsageExtraUpdates(usage, tiers, balance, paygEnabled, now)
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		slog.Warn("cn_quota_persist_failed", "account_id", account.ID, "provider", PlatformMistral, "error", err)
	} else {
		result.Persisted = true
		balance.Persisted = true
	}
	clearMistralPaygCooldown(ctx, s.accountRepo, account, paygEnabled, now)
	return result, nil
}

func (s *CNProviderBalanceService) queryMistralBalance(ctx context.Context, account *Account) (*CNProviderBalanceResult, error) {
	client := newMistralUsageClient(s.cfg, s.httpUpstream, account, s.resolveProxyURL(ctx, account), "CN_BALANCE")
	if client.apiKey == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "CN_BALANCE_NO_APIKEY", "account api_key is empty")
	}
	now := time.Now().UTC()
	usage, err := client.fetch(ctx, false)
	if err != nil {
		status, message, ok := mistralFetchFailure(err)
		if !ok {
			if isApplicationError(err) {
				return nil, err
			}
			return nil, infraerrors.Newf(http.StatusBadGateway, "CN_BALANCE_REQUEST_FAILED", "%v", err)
		}
		return &CNProviderBalanceResult{
			Provider:   PlatformMistral,
			StatusCode: status,
			FetchedAt:  now.Unix(),
			Available:  true,
			Error:      message,
		}, nil
	}

	paygEnabled := usage.paygEnabled(account.Extra)
	tiers := usage.tiers()
	result := usage.balance(now, paygEnabled)
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, mistralUsageExtraUpdates(usage, tiers, result, paygEnabled, now)); err != nil {
		slog.Warn("cn_balance_persist_failed", "account_id", account.ID, "provider", PlatformMistral, "error", err)
	} else {
		result.Persisted = true
	}
	clearMistralPaygCooldown(ctx, s.accountRepo, account, paygEnabled, now)
	return result, nil
}
