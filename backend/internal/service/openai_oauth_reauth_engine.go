package service

import (
	"context"
	"net/url"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Only the authenticated worker receives these headers. Account views expose
// the engine name, never the remote service's authentication material.
func (s *OpenAIOAuthReauthService) sessionStudioConfig(ctx context.Context) (string, map[string]string, error) {
	cfg, err := loadOpenAIReloginConfig(ctx, s.settings)
	if err != nil {
		return "", nil, infraerrors.ServiceUnavailable("OPENAI_REAUTH_ENGINE_CONFIG_UNAVAILABLE", "Re-login service configuration is unavailable")
	}
	endpoint := strings.TrimSpace(cfg.ReloginEndpoint)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", nil, infraerrors.BadRequest("OPENAI_REAUTH_ENGINE_ENDPOINT_INVALID", "Configure a valid HTTPS re-login endpoint in Credential Operations")
	}
	if err := validateGuardHeaders(cfg.ReloginHeaders, "relogin_headers"); err != nil {
		return "", nil, infraerrors.BadRequest("OPENAI_REAUTH_ENGINE_HEADERS_INVALID", "Re-login service headers are invalid")
	}
	headers := make(map[string]string, len(cfg.ReloginHeaders))
	for key, value := range cfg.ReloginHeaders {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "host", "content-length", "transfer-encoding", "connection", "x-openai-reauth-worker-token":
			return "", nil, infraerrors.BadRequest("OPENAI_REAUTH_ENGINE_HEADERS_INVALID", "Re-login service headers contain a reserved name")
		}
		headers[key] = value
	}
	return endpoint, headers, nil
}
