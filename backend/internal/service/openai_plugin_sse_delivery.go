package service

import (
	"context"
	"net/http"
	"strings"
)

const openAIWSSSEAcceleratorPluginKey = "local.sub2api.openai-ws-sse-accelerator"

type openAIPluginSSEPreflightKey struct{}

// Only the verified local dispatch boundary can establish provenance. An
// ordinary upstream's identically named response header grants no capability.
func markOpenAIPluginSSEPreflight(resp *http.Response, req *http.Request, installation *PluginInstallation) {
	if resp == nil || req == nil || installation == nil ||
		installation.PluginKey != openAIWSSSEAcceleratorPluginKey || installation.SignatureStatus != PluginSignatureTrusted ||
		resp.StatusCode != http.StatusOK || resp.Header.Get("X-Sub2api-Plugin-Preflight-Sse") != "1" ||
		!strings.EqualFold(strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]), "text/event-stream") {
		return
	}
	responseRequest := resp.Request
	if responseRequest == nil {
		responseRequest = req
	}
	resp.Request = responseRequest.WithContext(context.WithValue(responseRequest.Context(), openAIPluginSSEPreflightKey{}, true))
}

func (s *OpenAIGatewayService) openAIPluginSSEEarlyEvents(resp *http.Response, account *Account) bool {
	if s == nil || s.cfg == nil || !s.cfg.Gateway.OpenAIPluginSSEEarlyEventsEnabled ||
		account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth ||
		resp == nil || resp.Request == nil {
		return false
	}
	trusted, _ := resp.Request.Context().Value(openAIPluginSSEPreflightKey{}).(bool)
	return trusted
}

func openAIPluginEarlyMetadata(payload []byte, eventType string) bool {
	return len(payload) > 0 && openAIStreamEventIsMetadata(eventType) && eventType != "keepalive"
}

// A valid JSON payload followed by another data line is not a complete JSON
// event. The metric parser can emit a payload before the blank line; delivery
// authorization must still be invalidated by any following data line.
type openAIPluginMetadataEvent struct {
	complete bool
	eligible bool
}

func (e *openAIPluginMetadataEvent) data(payload []byte, eventType string) {
	if e.complete {
		e.eligible = false
		return
	}
	if len(payload) > 0 {
		e.complete = true
		e.eligible = openAIPluginEarlyMetadata(payload, eventType)
	}
}

func (e *openAIPluginMetadataEvent) boundary() bool {
	eligible := e.eligible
	*e = openAIPluginMetadataEvent{}
	return eligible
}
