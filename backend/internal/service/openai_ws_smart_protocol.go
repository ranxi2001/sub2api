package service

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/tidwall/gjson"
)

const smartProtocolSessionKey = "openai_smart_protocol_session"

type smartProtocolContextKey struct{}
type smartProtocolStatefulContextKey struct{}

type openAISmartProtocolDecision struct {
	Mode            string `json:"mode"`
	Protocol        string `json:"protocol"`
	Candidate       string `json:"candidate"`
	Reason          string `json:"reason"`
	Source          string `json:"prediction_source"`
	PredictedOutput int    `json:"predicted_output,omitempty"`
	Samples         int    `json:"samples"`
}

type openAISmartProtocolSession struct {
	Decision     openAISmartProtocolDecision
	AccountID    int64
	ConnectionID string
	Revision     int64
	// LastResponse is owned by this downstream connection, never a global ID cache.
	LastResponse string
}

func smartProtocolStrict(ctx context.Context) bool {
	v, _ := ctx.Value(smartProtocolContextKey{}).(bool)
	return v
}

func smartProtocolStateful(ctx context.Context) bool {
	value, _ := ctx.Value(smartProtocolStatefulContextKey{}).(bool)
	return value
}

// This is positive native bridge evidence, not the scheduler's permissive
// unobserved capability default. Specialized HTTP adapters are deliberately not
// promoted to the native WS/HTTP bridge merely because they accept Responses.
func smartProtocolHTTPSupported(a *Account) bool {
	if a == nil || a.Platform != PlatformOpenAI || validateOpenAIConnectionKind(a) != nil {
		return false
	}
	if a.HasManagedOpenAIConnections() || a.AuthConnectionID != "" || a.IsSIWCEnabled() || a.IsExcelBPSEnabled() || a.IsCopilotSDKEnabled() || a.Extra["openai_prism_browser"] == true {
		return false
	}
	if a.Type == AccountTypeAPIKey {
		supported, ok := a.Extra["openai_responses_supported"].(bool)
		return ok && supported
	}
	// The native Codex HTTP builder uses this same registration. OAuth is not a
	// WS-only capability. Unknown registrations must not inherit this evidence.
	return a.Type == AccountTypeOAuth && (a.GetCredential("client_id") == "" || a.GetCredential("client_id") == openai.ClientID) && !a.IsOpenAIPersonalAccessToken() && !a.IsOpenAIAgentIdentity()
}

func (s *OpenAIGatewayService) decideOpenAIWSUpstreamProtocol(a *Account, payload []byte) openAISmartProtocolDecision {
	cfg := s.cfg.Gateway.OpenAIWS.SmartProtocol
	mode := cfg.Mode
	if mode == "" {
		mode = "auto"
	}
	d := openAISmartProtocolDecision{Mode: mode, Protocol: "ws", Candidate: "ws", Reason: "small_or_unknown", Source: "cold_start"}
	if !cfg.Enabled {
		d.Reason = "disabled"
		return d
	}
	if !s.openAIWSHTTPBridgeEnabled() {
		d.Reason = "bridge_disabled"
		return d
	}
	if !smartProtocolHTTPSupported(a) {
		d.Reason = "http_capability_unconfirmed"
		return d
	}
	// Native Codex HTTP normalizes store=false; its ordinary HTTP forwarder
	// removes previous_response_id. Endpoint support is not chain support.
	// Stateless CN adapters also delete previous IDs at their final send boundary.
	if a.Type != AccountTypeAPIKey || a.UsesNativeCNResponses() || gjson.GetBytes(payload, "store").Type != gjson.True {
		d.Reason = "http_chain_unconfirmed"
		return d
	}
	if strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String()) != "" {
		d.Reason = "existing_chain"
		return d
	}
	if mode == "ws" {
		d.Reason = "manual_ws"
		return d
	}
	if mode == "http" {
		d.Protocol = "http"
		d.Candidate = "http"
		d.Reason = "manual_http"
		return d
	}
	threshold := cfg.LargeOutputThreshold
	if threshold == 0 {
		threshold = 2000
	}
	longBytes := cfg.LongContextBytes
	if longBytes == 0 {
		longBytes = 200000
	}
	key := smartProtocolKey(a, payload, "ws")
	if p90, n := s.smartProtocolHistory.p90(key, time.Now()); n >= 20 {
		d.PredictedOutput, d.Samples, d.Source = p90, n, "empirical_p90"
		if p90 >= threshold {
			d.Candidate, d.Reason = "http", "historical_large_output"
		}
	} else {
		d.Samples = n
	}
	// A requested cap is an upper bound, not an observed output distribution.
	if d.Candidate != "http" && gjson.GetBytes(payload, "max_output_tokens").Int() >= int64(threshold) {
		d.Candidate, d.Reason = "http", "large_output_cap"
		d.Source = "heuristic_cap"
	}
	effort := key.Effort
	if d.Candidate != "http" && (effort == "max" || effort == "xhigh") && len(gjson.GetBytes(payload, "input").Raw) >= longBytes {
		d.Candidate, d.Reason = "http", "high_effort_long_context"
		d.Source = "heuristic_context"
	}
	if mode != "shadow" {
		d.Protocol = d.Candidate
	}
	return d
}

type openAISmartProtocolHistoryKey struct {
	AccountID    int64
	ConnectionID string
	Revision     int64
	Model        string
	Effort       string
	Protocol     string
}

func smartProtocolKey(a *Account, payload []byte, protocol string) openAISmartProtocolHistoryKey {
	model := gjson.GetBytes(payload, "model").String()
	if a != nil {
		model = normalizeOpenAIModelForUpstream(a, a.GetMappedModel(model))
	}
	effort := ""
	if v := extractOpenAIReasoningEffortFromBody(payload, model, model); v != nil {
		effort = *v
	}
	return openAISmartProtocolHistoryKey{a.ID, a.AuthConnectionID, a.AuthConnectionRevision, model, effort, protocol}
}

type openAISmartProtocolSample struct {
	At     time.Time
	Output int
}
type openAISmartProtocolHistory struct {
	mu      sync.Mutex
	buckets map[openAISmartProtocolHistoryKey][]openAISmartProtocolSample
}

func (h *openAISmartProtocolHistory) observe(key openAISmartProtocolHistoryKey, output int, now time.Time) {
	if output <= 0 || len(key.Model) > 256 || len(key.Effort) > 32 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.buckets == nil {
		h.buckets = make(map[openAISmartProtocolHistoryKey][]openAISmartProtocolSample)
	}
	if _, exists := h.buckets[key]; !exists && len(h.buckets) >= 1024 {
		// Bounded eviction; no background task and no scan proportional to traffic.
		for k := range h.buckets {
			delete(h.buckets, k)
			break
		}
	}
	samples := h.buckets[key]
	if len(samples) >= 128 {
		copy(samples, samples[1:])
		samples = samples[:127]
	}
	h.buckets[key] = append(samples, openAISmartProtocolSample{now, output})
}
func (h *openAISmartProtocolHistory) p90(key openAISmartProtocolHistoryKey, now time.Time) (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	samples := h.buckets[key]
	values := make([]int, 0, len(samples))
	for _, sample := range samples {
		if now.Sub(sample.At) <= 15*time.Minute {
			values = append(values, sample.Output)
		}
	}
	if len(values) < 20 {
		return 0, len(values)
	}
	sort.Ints(values)
	return values[int(math.Ceil(float64(len(values))*0.9))-1], len(values)
}
