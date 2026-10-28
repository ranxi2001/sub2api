package service

import (
	"context"
	"encoding/json"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"net/url"
	"slices"
	"strings"
	"time"
)

type UpstreamOperationsFilter struct {
	From       time.Time `json:"from"`
	Until      time.Time `json:"until"`
	Upstream   string    `json:"upstream"`
	AccountID  int64     `json:"account_id"`
	GroupID    int64     `json:"group_id"`
	Model      string    `json:"model"`
	ModelScope string    `json:"model_scope"`
	Dimension  string    `json:"dimension"`
	Sort       string    `json:"sort"`
	Direction  string    `json:"direction"`
	Page       int       `json:"page"`
	PageSize   int       `json:"page_size"`
}

type UpstreamOperationsSnapshot struct {
	Scope       UpstreamOperationsFilter `json:"scope"`
	QueriedAt   time.Time                `json:"queried_at"`
	Attribution string                   `json:"attribution"`
	Currency    string                   `json:"currency"`
	Usage       json.RawMessage          `json:"usage"`
	Quality     ChannelOperationsSection `json:"quality"`
	Errors      ChannelOperationsSection `json:"errors"`
}

type UpstreamOperationsRepository interface {
	ReadUpstreamOperations(context.Context, UpstreamOperationsFilter) (*UpstreamOperationsSnapshot, error)
}

func ValidateUpstreamOperationsFilter(f UpstreamOperationsFilter) error {
	if f.From.IsZero() || !f.Until.After(f.From) || f.Until.Sub(f.From) > 31*24*time.Hour || f.Until.After(time.Now().Add(time.Minute)) || f.AccountID < 0 || f.GroupID < 0 || f.Page < 1 || f.Page > 1000000 || f.PageSize < 1 || f.PageSize > 100 || len(f.Model) > 200 || len(f.Upstream) > 2048 ||
		!slices.Contains([]string{"upstream", "account", "model"}, f.Dimension) || !slices.Contains([]string{"public", "recorded", "upstream"}, f.ModelScope) ||
		!slices.Contains([]string{"requests", "standard_charge", "request_income", "estimated_cost", "estimated_profit", "margin", "name"}, f.Sort) || !slices.Contains([]string{"asc", "desc"}, f.Direction) {
		return ErrChannelOperationsValidation
	}
	return nil
}
func (s *ChannelService) UpstreamOperations(ctx context.Context, f UpstreamOperationsFilter) (*UpstreamOperationsSnapshot, error) {
	if err := ValidateUpstreamOperationsFilter(f); err != nil {
		return nil, err
	}
	repo, ok := s.repo.(UpstreamOperationsRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("UPSTREAM_OPERATIONS_UNAVAILABLE", "Upstream operations data source unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	return repo.ReadUpstreamOperations(ctx, f)
}

// ResolveOperationsUpstream never claims the current account configuration is a
// historical URL snapshot. Unsupported or ambiguous dispatch stays unassigned.
func ResolveOperationsUpstream(a *Account) (string, string) {
	if a == nil {
		return "", "account_missing"
	}
	if a.ParentAccountID != nil {
		return "", "shadow_parent_unresolved"
	}
	var raw string
	switch {
	case a.IsGrok():
		text, media := a.GetGrokBaseURL(), a.GetGrokMediaBaseURL()
		if text != media {
			return "", "multiple_effective_bases"
		}
		raw = text
	case a.IsOpenAI() && a.Type != AccountTypeAPIKey && a.Type != AccountTypeUpstream:
		if a.IsSIWCEnabled() || a.Extra["openai_prism_browser"] == true || a.Extra["openai_excel_bps"] == true {
			return "", "oauth_dynamic_endpoint"
		}
		if a.Type == AccountTypeSetupToken && !a.IsOpenAIOAuthLike() {
			raw = "https://api.openai.com"
		} else if a.Type == AccountTypeOAuth || a.Type == AccountTypeSetupToken {
			raw = "https://chatgpt.com/backend-api/codex"
		} else {
			return "", "unsupported_account_type"
		}
	case a.IsMultiProtocolAPIKey():
		if a.IsAdaptiveAPIProtocol() {
			bases := map[string]bool{}
			for _, protocol := range []string{APIProtocolChatCompletions, APIProtocolAnthropic, APIProtocolResponses} {
				base := a.GetCNProtocolBaseURL(protocol)
				if base == "" {
					continue
				}
				clean, err := normalizeOperationsBaseURL(base)
				if err != nil {
					return "", "invalid_url"
				}
				bases[clean] = true
			}
			if len(bases) != 1 {
				return "", "multiple_effective_bases"
			}
			for base := range bases {
				return base, "current_config"
			}
		}
		if a.IsAnthropicProtocol() {
			raw = a.GetAnthropicProtocolBaseURL()
		} else if a.GetAPIProtocol() == APIProtocolResponses {
			raw = a.GetCNProtocolBaseURL(APIProtocolResponses)
		} else {
			raw = a.GetOpenAIBaseURL()
		}
	case a.IsOpenAI():
		raw = a.GetOpenAIBaseURL()
	case a.Platform == PlatformTypeSafe:
		raw = a.GetTypeSafeBaseURL()
	case a.Platform == PlatformAnthropic:
		if a.IsAnthropicOAuthOrSetupToken() {
			raw = "https://api.anthropic.com"
			if a.IsCustomBaseURLEnabled() && a.GetCustomBaseURL() != "" {
				raw = a.GetCustomBaseURL()
			}
		} else if a.Type == AccountTypeAPIKey || a.Type == AccountTypeUpstream {
			raw = a.GetBaseURL()
		}
	case a.Platform == PlatformGemini || a.Platform == PlatformAntigravity:
		if a.Type != AccountTypeAPIKey {
			return "", "oauth_dynamic_endpoint"
		}
		raw = a.GetGeminiBaseURL(geminicli.AIStudioBaseURL)
	}
	if raw == "" {
		return "", "unsupported_account_type"
	}
	clean, err := normalizeOperationsBaseURL(raw)
	if err != nil {
		return "", "invalid_url"
	}
	return clean, "current_config"
}

func normalizeOperationsBaseURL(raw string) (string, error) {
	if len(raw) > 2048 {
		return "", ErrChannelOperationsValidation
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", ErrChannelOperationsValidation
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", ErrChannelOperationsValidation
	}
	// Preserve ports, escaped path boundaries and path case. Forwarding builders
	// append endpoints after trimming trailing slash; do not collapse /v1 or tenants.
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u.String(), nil
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
