package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const UpstreamBillingProviderExtraKey = "upstream_billing_provider"

var ErrNewAPIConfiguration = infraerrors.BadRequest("NEW_API_CONFIGURATION_INVALID", "New API authorization or account verification failed")
var ErrNewAPIEncryptionKey = infraerrors.ServiceUnavailable("NEW_API_ENCRYPTION_KEY_REQUIRED", "Configure a fixed TOTP encryption key before saving New API authorization")

func newAPIError(reason string) error {
	return infraerrors.BadRequest("NEW_API_VERIFICATION_FAILED", "New API verification failed: "+reason)
}

// CanonicalNewAPISite preserves the origin and tenant path. Only a terminal v1
// segment, trailing slash and the default port are insignificant.
func CanonicalNewAPISite(raw string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Opaque != "" {
		return "", ErrNewAPIConfiguration
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return "", ErrNewAPIConfiguration
		}
		if (u.Scheme == "https" && n == 443) || (u.Scheme == "http" && n == 80) {
			port = ""
		}
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else {
		u.Host = host
		if strings.Contains(host, ":") {
			u.Host = "[" + host + "]"
		}
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	path = strings.TrimSuffix(path, "/v1")
	path = strings.TrimRight(path, "/")
	decoded, e := url.PathUnescape(path)
	if e != nil {
		return "", ErrNewAPIConfiguration
	}
	u.Path = decoded
	u.RawPath = path
	return u.String(), nil
}
func NewAPIAccountFingerprint(a *Account) string {
	b, _ := json.Marshal(upstreamBillingProbeIdentity(a))
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func newAPIKey(raw string) string { return strings.TrimPrefix(strings.TrimSpace(raw), "sk-") }
func maskNewAPIKey(raw string) string {
	k := newAPIKey(raw)
	if len(k) < 8 {
		return ""
	}
	return k[:4] + "**********" + k[len(k)-4:]
}

type NewAPIToken struct {
	ID              int64    `json:"id"`
	Name            string   `json:"name"`
	Key             string   `json:"key"`
	Group           string   `json:"group"`
	CrossGroupRetry bool     `json:"cross_group_retry"`
	RouteGroups     []string `json:"route_groups"`
}
type newAPIData struct {
	wallet     float64
	walletUnit string
	rawWallet  float64
	walletRate float64
	ratios     map[string]float64
	tokens     []NewAPIToken
}

func matchNewAPITokens(a *Account, tokens []NewAPIToken) []NewAPIToken {
	key := newAPIKey(a.GetCredential("api_key"))
	mask := maskNewAPIKey(key)
	matches := []NewAPIToken{}
	for _, t := range tokens {
		if t.ID > 0 && key != "" && (newAPIKey(t.Key) == key || (mask != "" && newAPIKey(t.Key) == mask)) {
			matches = append(matches, t)
		}
	}
	return matches
}

// User credentials are applied last, so account relay-key header overrides
// cannot impersonate a different management user.
func (s *UpstreamBillingProbeService) newAPIRequest(ctx context.Context, a *Account, site string, userID int64, secret, method, path string, out any) error {
	if s.accountTestService == nil || s.accountTestService.httpUpstream == nil {
		return newAPIError("transport_unavailable")
	}
	if _, e := s.accountTestService.validateUpstreamBaseURL(site); e != nil {
		return newAPIError("invalid_site_url")
	}
	proxy := ""
	if a.ProxyID != nil {
		if a.Proxy == nil || a.Proxy.ID != *a.ProxyID {
			return newAPIError("proxy_unavailable")
		}
		proxy = a.Proxy.URL()
	}
	reqCtx, cancel := context.WithTimeout(ctx, upstreamBillingProbeRequestTimeout)
	defer cancel()
	req, e := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(reqCtx, HTTPUpstreamProfileDefault)), method, site+path, bytes.NewReader(nil))
	if e != nil {
		return newAPIError("request_build_failed")
	}
	// Identify these management queries instead of inheriting Go's generic UA,
	// which some upstream gateways reject on token inventory routes.
	// Explicit account overrides still take precedence.
	req.Header.Set("User-Agent", "Sub2API-Upstream-Probe/1.0")
	a.ApplyHeaderOverrides(withoutHeaderOverrideRequestScope(req.Context()), req.Header)
	for key := range req.Header {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "New-Api-User") || strings.EqualFold(key, "Cookie") {
			delete(req.Header, key)
		}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("New-Api-User", strconv.FormatInt(userID, 10))
	var tlsProfile *tlsfingerprint.Profile
	if s.accountTestService.tlsFPProfileService != nil {
		tlsProfile = s.accountTestService.tlsFPProfileService.ResolveTLSProfile(a)
	}
	resp, e := s.accountTestService.httpUpstream.DoWithTLS(req, proxy, a.ID, a.Concurrency, tlsProfile)
	if e != nil {
		return newAPIError("request_failed")
	}
	if resp == nil || resp.Body == nil {
		return newAPIError("empty_response")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newAPIError("http_error")
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if e != nil || len(b) > 2*1024*1024 {
		return newAPIError("invalid_body")
	}
	if e = json.Unmarshal(b, out); e != nil {
		return newAPIError("invalid_response")
	}
	return nil
}
func (s *UpstreamBillingProbeService) fetchNewAPI(ctx context.Context, a *Account, site string, userID int64, secret string) (*newAPIData, error) {
	if userID <= 0 || secret == "" {
		return nil, ErrNewAPIConfiguration
	}
	var status struct {
		Success bool                 `json:"success"`
		Data    newAPIWalletSettings `json:"data"`
	}
	if e := s.newAPIRequest(ctx, a, site, userID, secret, "GET", "/api/status", &status); e != nil {
		return nil, e
	}
	unit := status.Data.QuotaPerUnit
	if !status.Success || unit == nil || !newAPIFinite(*unit) || *unit <= 0 || *unit > 1e15 {
		return nil, newAPIError("invalid_quota_conversion")
	}
	var user struct {
		Success bool `json:"success"`
		Data    struct {
			ID    int64    `json:"id"`
			Quota *float64 `json:"quota"`
		} `json:"data"`
	}
	if e := s.newAPIRequest(ctx, a, site, userID, secret, "GET", "/api/user/self", &user); e != nil {
		return nil, e
	}
	if !user.Success || user.Data.ID != userID || user.Data.Quota == nil || !newAPIFinite(*user.Data.Quota) || math.Abs(*user.Data.Quota) > 1e18 {
		return nil, newAPIError("invalid_user_wallet")
	}
	wallet := *user.Data.Quota / *unit
	if !newAPIFinite(wallet) || math.Abs(wallet) > 1e12 {
		return nil, newAPIError("invalid_user_wallet")
	}
	amount, currency, exchangeRate := status.Data.displayWallet(wallet)
	result := &newAPIData{wallet: amount, walletUnit: currency, rawWallet: wallet, walletRate: exchangeRate, ratios: map[string]float64{}, tokens: []NewAPIToken{}}
	// A missing pricing endpoint does not erase a successfully verified wallet.
	var pricing struct {
		Success bool               `json:"success"`
		Ratios  map[string]float64 `json:"group_ratio"`
	}
	if e := s.newAPIRequest(ctx, a, site, userID, secret, "GET", "/api/pricing", &pricing); e == nil && pricing.Success {
		for g, r := range pricing.Ratios {
			if g != "" && g != "auto" && len(g) <= 128 && newAPIFinite(r) && r >= 0 && r <= 1000000 {
				result.ratios[g] = r
			}
		}
	}
	seen := map[int64]bool{}
	queryPage := 0
	expectedTotal := -1
	for iteration := 0; iteration < 100; iteration++ {
		p := queryPage
		queryPage++
		var inventory struct {
			Success bool            `json:"success"`
			Data    json.RawMessage `json:"data"`
		}
		if e := s.newAPIRequest(ctx, a, site, userID, secret, "GET", fmt.Sprintf("/api/token/?p=%d&page_size=100", p), &inventory); e != nil {
			return nil, e
		}
		if !inventory.Success {
			return nil, newAPIError("invalid_inventory")
		}
		var items []NewAPIToken
		total := -1
		if len(inventory.Data) > 0 && inventory.Data[0] == '[' {
			if e := json.Unmarshal(inventory.Data, &items); e != nil {
				return nil, newAPIError("invalid_inventory")
			}
		} else {
			var page struct {
				Items    []NewAPIToken `json:"items"`
				Total    *int          `json:"total"`
				P        *int          `json:"p"`
				Page     *int          `json:"page"`
				PageSize *int          `json:"page_size"`
			}
			if e := json.Unmarshal(inventory.Data, &page); e != nil {
				return nil, newAPIError("invalid_inventory_pagination")
			}
			if page.P == nil {
				page.P = page.Page
			}
			if page.Total == nil || page.P == nil || page.PageSize == nil || (*page.P != p && (iteration != 0 || p != 0 || *page.P != 1)) || *page.PageSize <= 0 || *page.PageSize > 100 || *page.Total < 0 || *page.Total > 10000 || len(page.Items) > *page.PageSize || (expectedTotal >= 0 && expectedTotal != *page.Total) {
				return nil, newAPIError("invalid_inventory_pagination")
			}
			items = page.Items
			total = *page.Total
			expectedTotal = total
			queryPage = *page.P + 1
		}
		if len(items) > 1000 {
			return nil, newAPIError("inventory_limit")
		}
		for _, t := range items {
			if t.ID <= 0 || seen[t.ID] || len(t.Key) > 512 || len(t.Group) > 128 || len(t.Name) > 1024 {
				return nil, newAPIError("invalid_inventory_pagination")
			}
			seen[t.ID] = true
			result.tokens = append(result.tokens, t)
		}
		if total >= 0 {
			if len(result.tokens) == total {
				return result, nil
			}
			if len(result.tokens) > total || len(items) == 0 {
				return nil, newAPIError("incomplete_inventory")
			}
		} else if len(items) == 0 {
			return result, nil
		}
		if len(result.tokens) > 10000 {
			return nil, newAPIError("inventory_limit")
		}
	}
	return nil, newAPIError("incomplete_inventory")
}
func newAPIFinite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
func newAPISnapshot(a *Account, t NewAPIToken, d *newAPIData, interval int, now time.Time) *UpstreamBillingProbeSnapshot {
	fresh := now.Add(2 * time.Duration(interval) * time.Minute)
	unit := d.walletUnit
	if unit == "" {
		unit = "USD"
	}
	balance := map[string]any{"is_valid": true, "mode": "unrestricted", "wallet_balance": d.wallet, "remaining": d.wallet, "unit": unit, "source": "new_api"}
	if d.walletRate > 0 {
		balance["raw_wallet_balance"] = d.rawWallet
		balance["raw_unit"] = "USD"
		balance["display_exchange_rate"] = d.walletRate
	}
	snap := &UpstreamBillingProbeSnapshot{Status: UpstreamBillingProbeStatusUnsupported, LastAttemptAt: now, NextProbeAt: now.Add(nextProbeDelay(interval, 0)), LastError: "unsupported_group", Data: map[string]any{"provider": "new_api", "group": sanitizeUpstreamBalanceText(t.Group, 128), "token_id": t.ID, "object": "new_api.group_billing", "schema_version": 1, "billing_scope": "group"}, Balance: &UpstreamBalanceSnapshot{Status: UpstreamBillingProbeStatusOK, LastAttemptAt: now, ReceivedAt: probeTimePtr(now), FreshUntil: probeTimePtr(fresh), Data: balance}}
	if rate, ok := d.ratios[t.Group]; ok && t.Group != "auto" && !t.CrossGroupRetry && len(t.RouteGroups) <= 1 {
		snap.Status = UpstreamBillingProbeStatusOK
		snap.LastError = ""
		snap.ReceivedAt = probeTimePtr(now)
		snap.FreshUntil = probeTimePtr(fresh)
		for _, k := range []string{"group_rate_multiplier", "resolved_rate_multiplier", "effective_rate_multiplier"} {
			snap.Data[k] = rate
		}
		snap.Data["peak_rate_enabled"] = false
	}
	if scale := NewAPICostScale(a); NewAPICostSyncEnabled(a) {
		if cost, ok := NewAPICostToSync(snap, scale); ok {
			snap.Data["cost_multiplier_scale"] = scale
			snap.Data["cost_multiplier"] = cost
		}
	}
	return snap
}
