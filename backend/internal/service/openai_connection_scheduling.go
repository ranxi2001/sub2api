package service

import (
	"context"

	"github.com/gin-gonic/gin"
)

// Kept outside openai_auth_connection*.go: the connection account-type
// regression workflow restores those files to an older baseline and must
// still compile this scheduling bridge against them.

type openAIConnectionCapabilityContextKey struct{}

// WithOpenAIConnectionCapabilityForRequest records, for account selection, the
// connection capability that prepareAuthConnection will require for this
// inbound route. The scheduler expresses Responses turns with abstract
// capabilities (chat_completions for ordinary turns so Chat-only API key
// upstreams stay eligible, responses_compact for native compaction), while a
// managed parent is projected onto one connection by inbound route. Without
// this, a managed BPS grant, which advertises only native responses, is
// filtered before the send path that would accept it.
func WithOpenAIConnectionCapabilityForRequest(ctx context.Context, c *gin.Context) context.Context {
	if ctx == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		return ctx
	}
	return context.WithValue(ctx, openAIConnectionCapabilityContextKey{}, connectionCapabilityForRequest(c))
}

func openAIConnectionCapabilityFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	capability, ok := ctx.Value(openAIConnectionCapabilityContextKey{}).(string)
	return capability, ok
}

// supportsOpenAIEndpointCapabilityForRequest is the scheduling form of
// SupportsOpenAIEndpointCapability. For an unprojected managed parent and a
// Responses-family requirement it applies the same connection predicate as
// prepareAuthConnection; every other account and capability is unchanged.
func (a *Account) supportsOpenAIEndpointCapabilityForRequest(ctx context.Context, capability OpenAIEndpointCapability) bool {
	if a.HasManagedOpenAIConnections() && a.AuthConnectionID == "" {
		switch capability {
		case OpenAIEndpointCapabilityChatCompletions, OpenAIEndpointCapabilityResponses, OpenAIEndpointCapabilityResponsesCompact:
			if required, ok := openAIConnectionCapabilityFromContext(ctx); ok {
				return a.managedConnectionSupports(required)
			}
		}
	}
	return a.SupportsOpenAIEndpointCapability(capability)
}
