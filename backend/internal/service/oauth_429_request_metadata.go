package service

import (
	"context"
	"strings"

	"github.com/tidwall/gjson"
)

type oauth429WireModelKey struct{}

// ResolveOAuth429TargetModel uses the forwarding contract for this account and
// endpoint. It deliberately does not infer a different allowed public alias.
// Admission of the original input remains a separate, mandatory check.
func ResolveOAuth429TargetModel(account *Account, requested, endpoint string) string {
	if account == nil {
		return ""
	}
	if account.Platform == PlatformOpenAI {
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
