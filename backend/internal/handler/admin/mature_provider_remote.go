// Adapted from mizaawa/zayuapi v0.2.219, a71aa9615ccb55a19973908c9f261b5ba25a48c6.
// LGPL v3; upstream authors retain their copyrights. See mature_provider_NOTICE.md.
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

var errProviderAuthentication = errors.New("provider authentication failed")
var errProviderResponse = errors.New("provider returned an unsupported response")
var errProviderTurnstile = errors.New("provider requires Turnstile verification; use an existing dashboard Access Token or complete verification on the provider site")
var errProviderTwoFactor = errors.New("provider requires two-factor verification; use an existing dashboard Access Token")

// Current New API releases name why they refused a dashboard credential. The
// expired and retired cases are still failed logins.
var errProviderTokenExpired = fmt.Errorf("%w: the access token has expired", errProviderAuthentication)
var errProviderTokenRetired = fmt.Errorf("%w: legacy system access tokens have been retired", errProviderAuthentication)

// providerScopeError is a New API personal access token that signs in but
// lacks the permission a request needs. It is not a failed login.
type providerScopeError struct{ scope string }

func (e *providerScopeError) Error() string {
	if e.scope == "" {
		return "provider access token lacks a required permission"
	}
	return "provider access token lacks the " + e.scope + " permission"
}

type providerToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
}

func validProviderTokenType(value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		letter := char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z'
		if index == 0 && !letter {
			return false
		}
		if !letter && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func encodeProviderToken(accessToken, refreshToken, tokenType string) (string, error) {
	accessToken = strings.TrimSpace(accessToken)
	refreshToken = strings.TrimSpace(refreshToken)
	tokenType = strings.TrimSpace(tokenType)
	if accessToken == "" && refreshToken == "" {
		return "", errors.New("dashboard Access Token or Refresh Token is required")
	}
	if tokenType == "" {
		tokenType = "Bearer"
	}
	if !validProviderTokenType(tokenType) {
		return "", errors.New("invalid dashboard Token Type")
	}
	if len(accessToken) > 8192 || len(refreshToken) > 8192 {
		return "", errors.New("login credential is too long")
	}
	if !validProviderCredential(accessToken) || !validProviderCredential(refreshToken) {
		return "", errors.New("login credential contains invalid characters")
	}
	data, err := json.Marshal(providerToken{AccessToken: accessToken, RefreshToken: refreshToken, TokenType: tokenType})
	return string(data), err
}

func decodeProviderToken(secret string) (providerToken, error) {
	if strings.HasPrefix(secret, "{") {
		var token providerToken
		if err := json.Unmarshal([]byte(secret), &token); err != nil || (token.AccessToken == "" && token.RefreshToken == "") {
			return providerToken{}, errors.New("stored dashboard token is invalid")
		}
		if !validProviderTokenType(token.TokenType) {
			return providerToken{}, errors.New("stored dashboard token type is unsupported")
		}
		return token, nil
	}
	// Existing manually entered dashboard tokens from the earlier version.
	if !validProviderCredential(secret) {
		return providerToken{}, errors.New("stored dashboard token is invalid")
	}
	return providerToken{AccessToken: secret, TokenType: "Bearer"}, nil
}

func providerPublicError(err error) string {
	var scope *providerScopeError
	switch {
	case errors.Is(err, errProviderTurnstile):
		return errProviderTurnstile.Error()
	case errors.Is(err, errProviderTwoFactor):
		return errProviderTwoFactor.Error()
	case errors.As(err, &scope):
		permission := "a required permission"
		if scope.scope != "" {
			permission = "the " + scope.scope + " permission"
		}
		return "Provider access token lacks " + permission + "; grant it to the token on the provider site"
	case errors.Is(err, errProviderTokenExpired):
		return "Provider login failed: the access token has expired; create a new one on the provider site"
	case errors.Is(err, errProviderTokenRetired):
		return "Provider login failed: legacy system access tokens have been retired; create a personal access token (nap_…) in the provider's security settings"
	case errors.Is(err, errProviderStoredLogin):
		return "The saved provider login could not be read; edit the provider and sign in again"
	case errors.Is(err, errProviderAuthentication):
		return "Provider login failed: invalid or expired dashboard credential"
	case errors.Is(err, errProviderResponse):
		return "Provider returned an unsupported response; check the provider type and site address"
	default:
		var remote *providerRemoteError
		if errors.As(err, &remote) {
			return fmt.Sprintf("Provider %s returned HTTP %d", remote.operation, remote.status)
		}
		return "Could not connect to the provider; check its HTTPS address and availability"
	}
}

type providerRemoteError struct {
	operation string
	status    int
	// retryAfter is set only for HTTP 429 when the upstream sent a positive
	// Retry-After. It is a duration, never a credential or response body.
	retryAfter time.Duration
}

func (e *providerRemoteError) Error() string {
	return fmt.Sprintf("provider %s returned HTTP %d", e.operation, e.status)
}

func normalizeProviderURL(raw string) (string, error) {
	base, err := urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{})
	if err != nil {
		return "", errors.New("provider must use a public HTTPS URL")
	}
	u, err := url.Parse(base)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Hostname() == "" {
		return "", errors.New("provider URL must be an HTTPS site origin")
	}
	return strings.TrimRight(base, "/"), nil
}

// A request is bound to a validated public IP at dial time, closing the usual
// resolve-then-connect DNS rebinding gap. Redirects and environment proxies are
// disabled so credentials cannot be forwarded to another origin.
func providerHTTPClient() (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:                 nil,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConnsPerHost:   2,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil || len(addresses) == 0 {
				return nil, errors.New("provider DNS lookup failed")
			}
			for _, address := range addresses {
				ip, ok := netip.AddrFromSlice(address.IP)
				if !ok || !allowedProviderIP(ip.Unmap()) {
					return nil, errors.New("provider resolves to a non-public address")
				}
			}
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
		},
	}
	return &http.Client{
		Timeout:       15 * time.Second,
		Jar:           jar,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// providerBlockedPrefixes covers special-purpose ranges that IsGlobalUnicast
// accepts. Translation prefixes (NAT64, 6to4, Teredo) are blocked because they
// can embed a private IPv4 address that a gateway would then reach.
var providerBlockedPrefixes = func() []netip.Prefix {
	blocks := []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:10::/28",
		"2001:20::/28", "2001:db8::/32", "2002::/16", "fc00::/7",
	}
	prefixes := make([]netip.Prefix, 0, len(blocks))
	for _, block := range blocks {
		prefixes = append(prefixes, netip.MustParsePrefix(block))
	}
	return prefixes
}()

func allowedProviderIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range providerBlockedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// validProviderCredential rejects values that would be unsafe or invalid
// inside an HTTP header, such as CR/LF or other control characters.
func validProviderCredential(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

type providerRemote struct {
	client    *http.Client
	base      string
	token     string
	tokenType string
	// userID is sent as New-Api-User, which only older New API sign-ins need.
	userID string
}

// authorize adds the dashboard credential chosen at sign-in to a request.
func (r *providerRemote) authorize(req *http.Request) {
	if r.token != "" {
		tokenType := r.tokenType
		if tokenType == "" {
			tokenType = "Bearer"
		}
		req.Header.Set("Authorization", tokenType+" "+r.token)
	}
	if r.userID != "" {
		req.Header.Set("New-Api-User", r.userID)
	}
}

// providerAnonymousPath lists requests that never carry the dashboard
// credential: the Sub2API refresh exchange and New API's public endpoints.
func providerAnonymousPath(path string) bool {
	switch path {
	case "/api/v1/auth/refresh", "/api/status", "/api/user/login/encryption-key":
		return true
	}
	return false
}

func (r *providerRemote) request(ctx context.Context, method, path string, body any) (map[string]any, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.base+path, reader)
	if err != nil {
		return nil, errProviderResponse
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !providerAnonymousPath(path) {
		r.authorize(req)
	}
	res, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider request failed: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	var envelope map[string]any
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&envelope); err != nil {
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return nil, errProviderAuthentication
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return nil, &providerRemoteError{operation: path, status: res.StatusCode, retryAfter: providerRetryAfter(res)}
		}
		return nil, errProviderResponse
	}
	message, _ := envelope["message"].(string)
	if envelope["reason"] == "TURNSTILE_VERIFICATION_FAILED" || envelope["code"] == "TURNSTILE_VERIFICATION_FAILED" || strings.Contains(strings.ToLower(message), "turnstile") {
		return nil, errProviderTurnstile
	}
	if err := newAPIAccessError(envelope); err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, errProviderAuthentication
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, &providerRemoteError{operation: path, status: res.StatusCode, retryAfter: providerRetryAfter(res)}
	}
	if success, ok := envelope["success"].(bool); ok && !success {
		return nil, errProviderAuthentication
	}
	if code, ok := number(envelope["code"]); ok && code != 0 {
		return nil, errProviderAuthentication
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		return nil, errProviderResponse
	}
	return data, nil
}

func number(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	case string:
		n, err := strconv.ParseFloat(v, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func field(data map[string]any, keys ...string) *float64 {
	for _, key := range keys {
		if value, ok := number(data[key]); ok && value >= 0 && value < 1e12 {
			return &value
		}
	}
	return nil
}

// Balances can be debts. Spend/recharge remain nonnegative, but a signed
// balance is an observed value and must still participate in low alerts.
func providerBalanceField(data map[string]any, key string) *float64 {
	if value, ok := number(data[key]); ok && value > -1e12 && value < 1e12 {
		return &value
	}
	return nil
}

func divide(value *float64, factor float64) *float64 {
	if value == nil || factor <= 0 {
		return nil
	}
	converted := *value / factor
	return &converted
}

// providerSpendWindow is the half-open interval a profit read wants priced.
// The zero value keeps the channel-operations today probe unchanged.
type providerSpendWindow struct {
	From, Until time.Time
}

func (w providerSpendWindow) active() bool {
	return !w.From.IsZero() && w.Until.After(w.From)
}

// providerProbe is what one balance probe learned about a provider account.
type providerProbe struct {
	metrics providerMetrics
	// secret is a renewed Sub2API token pair; empty when the login is unchanged.
	secret string
	// userID is the account ID that New API reports in /api/user/self.
	userID string
	// spend is the wallet amount for the requested window. It is not written
	// into today_spend unless the window is the current consumption day.
	spend         *float64
	spendCurrency string
	spendSource   string
}

func probeProviderWithClient(ctx context.Context, provider providerRecord, secret string, client *http.Client) (providerMetrics, error) {
	probe, err := probeProviderAccount(ctx, provider, secret, client)
	return probe.metrics, err
}

func probeProviderAccount(ctx context.Context, provider providerRecord, secret string, client *http.Client) (providerProbe, error) {
	return probeProviderAccountWindow(ctx, provider, secret, client, providerSpendWindow{})
}

func probeProviderSpend(ctx context.Context, provider providerRecord, secret string, client *http.Client, from, until time.Time) (providerProbe, error) {
	return probeProviderAccountWindow(ctx, provider, secret, client, providerSpendWindow{From: from, Until: until})
}

func providerRecordID(provider providerRecord) int64 {
	id, err := strconv.ParseInt(provider.ID, 10, 64)
	if err != nil || id < 1 {
		return 0
	}
	return id
}

// spendGet reads one wallet endpoint. A 429 uses the profit-sync backoff.
// A refresh token is exchanged at most once for this call, matching the
// channel-operations probe.
func (r *providerRemote) spendGet(ctx context.Context, providerID int64, path string, renew func() error) (map[string]any, error) {
	var data map[string]any
	renewed := false
	err := profitRetry(ctx, providerID, "wallet_spend", func() error {
		var err error
		data, err = r.request(ctx, http.MethodGet, path, nil)
		if errors.Is(err, errProviderAuthentication) && renew != nil && !renewed {
			renewed = true
			if err = renew(); err != nil {
				return err
			}
			data, err = r.request(ctx, http.MethodGet, path, nil)
		}
		return err
	})
	return data, err
}

func newAPIStatPath(from, until time.Time) string {
	end := until.Unix()
	if end <= from.Unix() {
		end = from.Unix() + 1
	}
	return fmt.Sprintf("/api/log/self/stat?type=2&start_timestamp=%d&end_timestamp=%d", from.Unix(), end)
}

// sumSnapshotActualCost totals period actual_cost from snapshot-v2. Trend is
// preferred. Model rows are used only when trend is absent or has no
// actual_cost. List-price cost is never substituted.
func sumSnapshotActualCost(data map[string]any) (*float64, error) {
	if sum, ok := seriesActualCost(data["trend"]); ok {
		return &sum, nil
	}
	if sum, ok := seriesActualCost(data["models"]); ok {
		return &sum, nil
	}
	return nil, errProviderResponse
}

func seriesActualCost(raw any) (float64, bool) {
	rows, ok := raw.([]any)
	if !ok {
		return 0, false
	}
	var sum float64
	for _, row := range rows {
		item, ok := row.(map[string]any)
		if !ok {
			return 0, false
		}
		if _, present := item["actual_cost"]; !present {
			return 0, false
		}
		value, ok := number(item["actual_cost"])
		if !ok || value < 0 || value >= 1e12 {
			return 0, false
		}
		sum += value
	}
	return sum, true
}

func sub2APIWindowSpend(ctx context.Context, r *providerRemote, providerID int64, renew func() error, from, until time.Time) (*float64, error) {
	start, end, ok := service.ProfitSnapshotDates(from, until)
	if !ok {
		return nil, errProviderResponse
	}
	query := url.Values{}
	query.Set("start_date", start)
	query.Set("end_date", end)
	query.Set("timezone", "Asia/Shanghai")
	query.Set("granularity", "day")
	query.Set("include_trend", "true")
	query.Set("include_model_stats", "true")
	query.Set("include_group_stats", "false")
	data, err := r.spendGet(ctx, providerID, "/api/v1/usage/dashboard/snapshot-v2?"+query.Encode(), renew)
	if err != nil {
		return nil, err
	}
	return sumSnapshotActualCost(data)
}

func probeProviderAccountWindow(ctx context.Context, provider providerRecord, secret string, client *http.Client, window providerSpendWindow) (providerProbe, error) {
	r := providerRemote{client: client, base: provider.BaseURL}
	var rotatedSecret string
	switch provider.SiteType {
	case "sub2api":
		var credential providerToken
		if provider.AuthMode == "password" {
			login, err := r.request(ctx, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": provider.Account, "password": secret})
			if err != nil {
				return providerProbe{secret: rotatedSecret}, err
			}
			r.token, _ = login["access_token"].(string)
			if r.token == "" {
				if required, _ := login["requires_2fa"].(bool); required {
					return providerProbe{secret: rotatedSecret}, errProviderTwoFactor
				}
				return providerProbe{secret: rotatedSecret}, errProviderResponse
			}
		} else {
			token, err := decodeProviderToken(secret)
			if err != nil {
				return providerProbe{secret: rotatedSecret}, err
			}
			credential = token
			r.token = token.AccessToken
			r.tokenType = token.TokenType
		}
		renew := func() error {
			if credential.RefreshToken == "" {
				return errProviderAuthentication
			}
			fresh, err := r.request(ctx, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": credential.RefreshToken})
			if err != nil {
				return err
			}
			access, _ := fresh["access_token"].(string)
			if access == "" {
				return errProviderResponse
			}
			refresh, _ := fresh["refresh_token"].(string)
			if refresh == "" {
				refresh = credential.RefreshToken
			}
			freshType, _ := fresh["token_type"].(string)
			if freshType == "" {
				freshType = credential.TokenType
			}
			rotatedSecret, err = encodeProviderToken(access, refresh, freshType)
			if err != nil {
				return err
			}
			credential.AccessToken, credential.RefreshToken = access, refresh
			r.token = access
			r.tokenType = freshType
			return nil
		}
		if provider.AuthMode != "password" && r.token == "" {
			if err := renew(); err != nil {
				return providerProbe{secret: rotatedSecret}, err
			}
		}
		profile, err := r.request(ctx, http.MethodGet, "/api/v1/user/profile", nil)
		if errors.Is(err, errProviderAuthentication) && credential.RefreshToken != "" {
			if err = renew(); err == nil {
				profile, err = r.request(ctx, http.MethodGet, "/api/v1/user/profile", nil)
			}
		}
		if err != nil {
			return providerProbe{secret: rotatedSecret}, err
		}
		providerID := providerRecordID(provider)
		var renewSpend func() error
		if credential.RefreshToken != "" {
			renewSpend = renew
		}
		balance := providerMetrics{
			Balance:       providerBalanceField(profile, "balance"),
			TotalRecharge: field(profile, "total_recharged"),
			Currency:      "USD",
		}
		// A historical window uses the dated user snapshot. It must not replace
		// the today card, which still comes from dashboard/stats.
		if window.active() && !service.ProfitWindowUsesWalletToday(window.From, window.Until, time.Now()) {
			spend, err := sub2APIWindowSpend(ctx, &r, providerID, renewSpend, window.From, window.Until)
			if err != nil {
				return providerProbe{metrics: balance, secret: rotatedSecret}, err
			}
			return providerProbe{metrics: balance, secret: rotatedSecret, spend: spend, spendCurrency: "USD", spendSource: "sub2api_snapshot_v2"}, nil
		}
		stats, err := r.spendGet(ctx, providerID, "/api/v1/usage/dashboard/stats", renewSpend)
		if err != nil {
			return providerProbe{secret: rotatedSecret}, err
		}
		balance.TodaySpend = field(stats, "today_actual_cost", "today_cost")
		balance.TotalSpend = field(stats, "total_actual_cost", "total_cost")
		return providerProbe{metrics: balance, secret: rotatedSecret, spend: balance.TodaySpend, spendCurrency: "USD", spendSource: "sub2api_dashboard_stats"}, nil
	case "newapi":
		// The probe signs in exactly as the key operations do.
		if err := newAPISignIn(ctx, &r, provider, secret); err != nil {
			return providerProbe{secret: rotatedSecret}, err
		}
		profile, err := r.request(ctx, http.MethodGet, "/api/user/self", nil)
		if err != nil {
			return providerProbe{secret: rotatedSecret}, err
		}
		status, err := r.request(ctx, http.MethodGet, "/api/status", nil)
		if err != nil {
			return providerProbe{secret: rotatedSecret}, err
		}
		quotaPerUnit, ok := number(status["quota_per_unit"])
		if !ok || quotaPerUnit <= 0 {
			return providerProbe{secret: rotatedSecret}, errors.New("the New API quota conversion is unavailable")
		}
		metrics := providerMetrics{
			Balance:    divide(providerBalanceField(profile, "quota"), quotaPerUnit),
			TotalSpend: divide(field(profile, "used_quota"), quotaPerUnit),
			Currency:   "USD",
		}
		// New API exposes a display preference independently of its quota
		// conversion. Convert only with an explicit exchange rate; otherwise
		// retain the source quota-unit label instead of inventing a currency.
		quotaScale := 1.0
		switch strings.ToUpper(providerText(status["quota_display_type"])) {
		case "USD":
			metrics.Currency = "USD"
		case "CNY":
			if rate, ok := number(status["usd_exchange_rate"]); ok && rate > 0 && rate < 1e6 {
				metrics.Currency = "CNY"
				quotaScale = rate
			} else {
				metrics.Currency = "quota_units"
			}
		default:
			metrics.Currency = "quota_units"
		}
		metrics.Balance = divide(metrics.Balance, 1/quotaScale)
		metrics.TotalSpend = divide(metrics.TotalSpend, 1/quotaScale)
		location, _ := time.LoadLocation("Asia/Shanghai")
		now := time.Now().In(location)
		statFrom := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
		statUntil := now.Add(time.Second)
		explicit := window.active()
		if explicit {
			statFrom, statUntil = window.From, window.Until
		}
		daily, statErr := r.spendGet(ctx, providerRecordID(provider), newAPIStatPath(statFrom, statUntil), nil)
		if explicit && statErr != nil {
			return providerProbe{metrics: metrics, secret: rotatedSecret, userID: newAPIAccountID(profile)}, statErr
		}
		if statErr == nil {
			spend := divide(field(daily, "quota"), quotaPerUnit/quotaScale)
			if !explicit || service.ProfitWindowUsesWalletToday(window.From, window.Until, time.Now()) {
				metrics.TodaySpend = spend
			}
			return providerProbe{metrics: metrics, secret: rotatedSecret, userID: newAPIAccountID(profile), spend: spend, spendCurrency: metrics.Currency, spendSource: "newapi_log_stat"}, nil
		}
		// New API self/profile has no canonical cumulative recharge field.
		// Leave it unknown instead of presenting quota + spend as payments.
		// A today-card probe still succeeds when the daily stat is unavailable.
		return providerProbe{metrics: metrics, userID: newAPIAccountID(profile)}, nil
	default:
		return providerProbe{secret: rotatedSecret}, errors.New("unsupported provider type")
	}
}
