package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"

	"github.com/tidwall/gjson"
)

type oauth429WireModelKey struct{}
type oauth429CompactModelKey struct{}
type oauth429RequestRouteKey struct{}

const SettingKeyOAuth429Routing = "smart_ops_oauth429_routing"

// Only the non-secret compact model selector and a durable change identity are
// stored. Never serialize the gateway configuration or upstream credentials.
type OAuth429RoutingPolicy struct {
	CompactModel string    `json:"compact_model"`
	Revision     string    `json:"revision"`
	ValidSince   time.Time `json:"valid_since"`
}

type OAuth429RoutingConfigurator interface {
	ConfigureOAuth429Routing(context.Context, string) (OAuth429RoutingPolicy, error)
}

type oauth429RequestRoute struct {
	Model   string
	Compact bool
}

func (s *OpenAIGatewayService) withOAuth429Routing(ctx context.Context, requested string, compact bool) context.Context {
	model := ""
	if s != nil && s.cfg != nil {
		model = strings.TrimSpace(s.cfg.Gateway.OpenAICompactModel)
	}
	ctx = context.WithValue(ctx, oauth429CompactModelKey{}, model)
	return context.WithValue(ctx, oauth429RequestRouteKey{}, oauth429RequestRoute{Model: requested, Compact: compact})
}

func oauth429CompactModel(ctx context.Context) string {
	model, _ := ctx.Value(oauth429CompactModelKey{}).(string)
	return model
}

func oauth429ConfiguredCompactModel(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Gateway.OpenAICompactModel)
}

func OAuth429RequestModelSupported(account *Account, requested, endpoint string, compactModel ...string) bool {
	return account != nil && supportsOpenAIRequestModel(account, requested, strings.HasSuffix(endpoint, "/responses/compact"), compactModel...)
}

// ResolveOAuth429TargetModel uses the forwarding contract for this account and
// endpoint. It deliberately does not infer a different allowed public alias.
// Admission of the original input remains a separate, mandatory check.
func ResolveOAuth429TargetModel(account *Account, requested, endpoint string, compactModel ...string) string {
	if account == nil {
		return ""
	}
	if account.Platform == PlatformOpenAI {
		if strings.HasSuffix(endpoint, "/responses/compact") && len(compactModel) > 0 && !account.IsCopilotSDKEnabled() {
			if target := resolveOpenAICompactFallbackModel(account, requested, compactModel[0]); target != "" {
				return target
			}
		}
		if endpoint == "" || strings.HasSuffix(endpoint, "/responses") || strings.HasSuffix(endpoint, "/responses/compact") {
			return resolveOpenAIAccountUpstreamModelForRequest(account, requested, strings.HasSuffix(endpoint, "/responses/compact"))
		}
		return normalizeOpenAIModelForUpstream(account, account.GetMappedModel(requested))
	}
	return account.GetMappedModel(requested)
}

// WithOAuth429WireModel binds the final model at request construction, before
// transport wrappers can replace GetBody or begin sending a streaming body.
// The public input model remains in ctxkey.Model for whitelist validation.
func WithOAuth429WireModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, oauth429WireModelKey{}, model)
}

func withOAuth429WireBody(ctx context.Context, body []byte) context.Context {
	model := gjson.GetBytes(body, "model").String()
	if model == "" {
		model = gjson.GetBytes(body, "request.model").String()
	}
	if model == "" {
		return ctx
	}
	return WithOAuth429WireModel(ctx, model)
}
