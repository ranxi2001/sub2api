package service

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Maintenance uses a saved member without changing the account's single proxy.
// Inference owns its independent least-in-flight lease for the logical request.
func resolveOpenAIMaintenanceProxy(ctx context.Context, account *Account, repo ProxyRepository, expectedURL ...string) (string, error) {
	if account == nil {
		return "", nil
	}
	route, hasRoute := openAIRequestProxyFromContext(ctx, account.ID)
	unavailable := func() (string, error) {
		return "", infraerrors.New(http.StatusServiceUnavailable, "OPENAI_PROXY_UNAVAILABLE", "No selected OpenAI proxy is available; direct fallback is disabled")
	}
	if !account.OpenAIRequestProxyEnabled() {
		// A saved mode change cannot replace an in-flight request's exit.
		if hasRoute {
			return unavailable()
		}
		return "", nil
	}
	if account.AuthConnectionID != "" || account.HasManagedOpenAIConnections() {
		return "", infraerrors.New(http.StatusServiceUnavailable, "OPENAI_PROXY_CONNECTION_CONFLICT", "Request proxies require an ordinary OAuth account without a fixed authorization connection")
	}
	if repo == nil {
		return unavailable()
	}
	if hasRoute {
		if _, err := ValidateOpenAIRequestProxyEgress(ctx, account.ID, route.proxy); err != nil {
			return "", err
		}
		if len(expectedURL) > 0 && expectedURL[0] != route.proxy {
			return unavailable()
		}
		expectedURL = []string{route.proxy}
	}
	now := time.Now()
	for _, id := range account.OpenAIRequestProxyIDs() {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		proxy, err := repo.GetByID(ctx, id)
		if err != nil || proxy == nil || !proxy.IsActive() || proxy.IsExpired(now) {
			continue
		}
		if strings.TrimSpace(proxy.Host) == "" || proxy.Port <= 0 || proxy.Port > 65535 {
			continue
		}
		switch strings.ToLower(proxy.Protocol) {
		case "http", "https", "socks5", "socks5h":
		default:
			continue
		}
		raw := proxy.URL()
		if len(expectedURL) > 0 && raw != expectedURL[0] {
			continue
		}
		parsed, err := url.Parse(raw)
		if err == nil && parsed.Hostname() != "" {
			if hasRoute {
				if _, err := ValidateOpenAIRequestProxyEgress(ctx, account.ID, raw); err != nil {
					return "", err
				}
			}
			return raw, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return unavailable()
}
