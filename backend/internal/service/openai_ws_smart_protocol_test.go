package service

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestSmartProtocolDecision(t *testing.T) {
	for _, tc := range []struct {
		name, mode, previous   string
		enabled, bridge, known bool
		cap                    int
		want, reason           string
	}{
		{"disabled", "auto", "", false, true, true, 8000, "ws", "disabled"},
		{"unknown", "http", "", true, true, false, 8000, "ws", "http_capability_unconfirmed"},
		{"ws_only", "http", "", true, true, false, 0, "ws", "http_capability_unconfirmed"},
		{"manual_http", "http", "", true, true, true, 0, "http", "manual_http"},
		{"manual_ws", "ws", "", true, true, true, 8000, "ws", "manual_ws"},
		{"shadow", "shadow", "", true, true, true, 8000, "ws", "large_output_cap"},
		{"auto", "auto", "", true, true, true, 8000, "http", "large_output_cap"},
		{"small", "auto", "", true, true, true, 100, "ws", "small_or_unknown"},
		{"chain", "http", "resp_prior", true, true, true, 8000, "ws", "existing_chain"},
		{"kill_bridge", "http", "", true, false, true, 8000, "ws", "bridge_disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = tc.bridge
			cfg.Gateway.OpenAIWS.SmartProtocol = config.GatewayOpenAIWSSmartProtocolConfig{Enabled: tc.enabled, Mode: tc.mode}
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{}}
			if tc.known {
				a.Extra["openai_responses_supported"] = true
			}
			payload := []byte(`{"model":"synthetic","store":true,"max_output_tokens":` + strconv.Itoa(tc.cap) + `,"previous_response_id":"` + tc.previous + `"}`)
			d := (&OpenAIGatewayService{cfg: cfg}).decideOpenAIWSUpstreamProtocol(a, payload)
			require.Equal(t, tc.want, d.Protocol)
			require.Equal(t, tc.reason, d.Reason)
			if tc.name == "shadow" {
				require.Equal(t, "http", d.Candidate)
				require.Equal(t, "heuristic_cap", d.Source)
				require.Zero(t, d.PredictedOutput)
			}
		})
	}
	t.Run("native codex supports HTTP", func(t *testing.T) {
		require.True(t, smartProtocolHTTPSupported(&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"client_id": openai.ClientID}}))
	})
}

func TestSmartProtocolHistory(t *testing.T) {
	var h openAISmartProtocolHistory
	a := &Account{ID: 1}
	payload := []byte(`{"model":"synthetic","reasoning":{"effort":"max"}}`)
	key := smartProtocolKey(a, payload, "ws")
	now := time.Now()
	for i := 1; i <= 19; i++ {
		h.observe(key, i*100, now)
	}
	p, n := h.p90(key, now)
	require.Zero(t, p)
	require.Equal(t, 19, n)
	h.observe(key, 2000, now)
	p, n = h.p90(key, now)
	require.Equal(t, 1800, p)
	require.Equal(t, 20, n)
	other := key
	other.Protocol = "http"
	_, n = h.p90(other, now)
	require.Zero(t, n)
	other = key
	other.ConnectionID = "another"
	_, n = h.p90(other, now)
	require.Zero(t, n)
	other = key
	other.Model = "different"
	_, n = h.p90(other, now)
	require.Zero(t, n)
	_, n = h.p90(key, now.Add(16*time.Minute))
	require.Zero(t, n)
	for i := 0; i < 200; i++ {
		h.observe(key, i+1, now)
	}
	require.Len(t, h.buckets[key], 128)
}

func TestSmartProtocolAuthorizationMatrix(t *testing.T) {
	cases := []struct {
		name    string
		account *Account
		http    bool
	}{
		{"confirmed API key", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_responses_supported": true}}, true},
		{"negative API key", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_responses_supported": false}}, false},
		{"unknown API key", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, false},
		{"native Codex", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"client_id": openai.ClientID}}, true},
		{"unknown registration", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"client_id": "unrecognized"}}, false},
		{"BPS registration", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"client_id": openai.ExcelClientID}}, false},
		{"managed projection", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, AuthConnectionID: "synthetic", Extra: map[string]any{"openai_responses_supported": true, "openai_connections_managed": true, "openai_connection_kind": "api_key"}}, false},
		{"identity only", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"openai_connection_kind": "siwc_identity"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.http, smartProtocolHTTPSupported(tc.account)) })
	}
}

func TestSmartProtocolNativeCodexEndpointIsNotChainEvidence(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
	cfg.Gateway.OpenAIWS.SmartProtocol = config.GatewayOpenAIWSSmartProtocolConfig{Enabled: true, Mode: "http"}
	a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"client_id": openai.ClientID}}
	require.True(t, smartProtocolHTTPSupported(a))
	decision := (&OpenAIGatewayService{cfg: cfg}).decideOpenAIWSUpstreamProtocol(a, []byte(`{"model":"synthetic","max_output_tokens":8000}`))
	require.Equal(t, "ws", decision.Protocol)
	require.Equal(t, "http_chain_unconfirmed", decision.Reason)
}

func TestSmartProtocolPredictionSources(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
	cfg.Gateway.OpenAIWS.SmartProtocol = config.GatewayOpenAIWSSmartProtocolConfig{Enabled: true, Mode: "auto"}
	a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_responses_supported": true}}
	svc := &OpenAIGatewayService{cfg: cfg}
	payload := []byte(`{"model":"synthetic","store":true,"reasoning":{"effort":"xhigh"},"input":"` + strings.Repeat("x", 200000) + `"}`)
	d := svc.decideOpenAIWSUpstreamProtocol(a, payload)
	require.Equal(t, "http", d.Protocol)
	require.Equal(t, "heuristic_context", d.Source)
	require.Zero(t, d.PredictedOutput)
	small := []byte(`{"model":"synthetic","store":true,"reasoning":{"effort":"xhigh"},"input":"small"}`)
	key := smartProtocolKey(a, small, "ws")
	for i := 0; i < 19; i++ {
		svc.smartProtocolHistory.observe(key, 8000, time.Now())
	}
	d = svc.decideOpenAIWSUpstreamProtocol(a, small)
	require.Equal(t, "ws", d.Protocol)
	require.Equal(t, "cold_start", d.Source)
	svc.smartProtocolHistory.observe(key, 8000, time.Now())
	d = svc.decideOpenAIWSUpstreamProtocol(a, small)
	require.Equal(t, "http", d.Protocol)
	require.Equal(t, "empirical_p90", d.Source)
	require.Equal(t, 8000, d.PredictedOutput)
}
