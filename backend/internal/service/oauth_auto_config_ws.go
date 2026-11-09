package service

import infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

func validateOAuthInitialWSMode(mode *string) error {
	if mode == nil {
		return nil
	}
	switch *mode {
	case OpenAIWSIngressModeOff, OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough, OpenAIWSIngressModeHTTPBridge:
		return nil
	default:
		return infraerrors.BadRequest("AUTO_CONFIG_INVALID", "OpenAI WS mode must be off, ctx_pool, passthrough or http_bridge")
	}
}

// Missing fields belong to legacy templates and do not take ownership of the
// imported transport settings. Explicit false/off values do override imports.
func applyOAuthInitialWS(extra map[string]any, c OAuthAutoConfig) {
	if c.OpenAIWSMode != nil {
		extra["openai_oauth_responses_websockets_v2_mode"] = *c.OpenAIWSMode
		extra["openai_oauth_responses_websockets_v2_enabled"] = *c.OpenAIWSMode != OpenAIWSIngressModeOff
	}
	if c.OpenAIWSSSEAcceleration != nil {
		extra["openai_oauth_ws_sse_acceleration"] = *c.OpenAIWSSSEAcceleration
	}
}
