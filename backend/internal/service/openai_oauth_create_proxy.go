package service

import (
	"context"
	"net/url"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// GenerateAuthURLWithProxySettings pins a request-mode authorization session to
// one currently available member. Single-proxy callers keep the existing flow.
func (s *OpenAIOAuthService) GenerateAuthURLWithProxySettings(ctx context.Context, proxyID *int64, proxyMode string, proxyIDs []int64, redirectURI, platform string, oauthClient ...string) (*OpenAIAuthURLResult, error) {
	requestProxy, err := openAIOAuthRequestProxySettings(proxyMode, proxyIDs)
	if err != nil {
		return nil, err
	}
	if !requestProxy {
		return s.GenerateAuthURL(ctx, proxyID, redirectURI, platform, oauthClient...)
	}
	selectedID, proxyURL, err := s.resolveOAuthRequestProxy(ctx, proxyIDs)
	if err != nil {
		return nil, err
	}
	result, err := s.generateAuthURLWithProxyURL(ctx, proxyURL, true, redirectURI, platform, oauthClient...)
	if err != nil {
		return nil, err
	}
	result.ProxyMode = OpenAIProxyModeRequest
	result.ProxyID = selectedID
	return result, nil
}

func openAIOAuthRequestProxySettings(mode string, ids []int64) (bool, error) {
	switch mode {
	case "", OpenAIProxyModeSingle:
		if len(ids) != 0 {
			return false, infraerrors.BadRequest("INVALID_OPENAI_PROXY_MODE", "OpenAI proxy IDs require request proxy mode")
		}
		return false, nil
	case OpenAIProxyModeRequest:
		if len(ids) == 0 {
			return false, infraerrors.BadRequest("OPENAI_PROXY_GROUP_REQUIRED", "Select at least one OpenAI proxy")
		}
		for _, id := range ids {
			if id <= 0 {
				return false, infraerrors.BadRequest("INVALID_OPENAI_PROXY_IDS", "OpenAI proxy IDs must be positive integers")
			}
		}
		return true, nil
	default:
		return false, infraerrors.BadRequest("INVALID_OPENAI_PROXY_MODE", "OpenAI proxy mode must be single or request")
	}
}

// expectedURL makes callback validation fail closed if the original exit is no
// longer an available member of the current group; it never selects a new exit.
func (s *OpenAIOAuthService) resolveOAuthRequestProxy(ctx context.Context, ids []int64, expectedURL ...string) (int64, string, error) {
	unavailable := func() (int64, string, error) {
		return 0, "", infraerrors.ServiceUnavailable("OPENAI_OAUTH_PROXY_UNAVAILABLE", "The authorization proxy is unavailable; generate a new authorization URL after checking the selected proxies")
	}
	if s.proxyRepo == nil {
		return unavailable()
	}
	now := time.Now()
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return 0, "", err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		proxy, err := s.proxyRepo.GetByID(ctx, id)
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
			return id, raw, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	return unavailable()
}
