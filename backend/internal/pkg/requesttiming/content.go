package requesttiming

import (
	"context"
	"encoding/json"
	"strings"
)

// Content records typed protocol observations without retaining generated text.
// The legacy first_semantic/first_visible meanings remain unchanged.
func Content(ctx context.Context, data []byte, eventTypes ...string) {
	if From(ctx) == nil {
		return
	}
	var event struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
		Text  string `json:"text"`
		Item  struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"item"`
	}
	if json.Unmarshal(data, &event) != nil {
		return
	}
	if event.Type == "" && len(eventTypes) > 0 {
		event.Type = strings.TrimSpace(eventTypes[0])
	}
	reasoning := false
	switch event.Type {
	case "response.output_item.added", "response.output_item.done":
		reasoning = event.Item.Type == "reasoning"
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		reasoning = event.Delta != ""
	case "response.reasoning_text.done", "response.reasoning_summary_text.done":
		reasoning = event.Text != ""
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		reasoning = true
	}
	visibleText := (event.Type == "response.output_text.delta" && event.Delta != "") || (event.Type == "response.output_text.done" && event.Text != "")
	if (event.Type == "response.output_item.added" || event.Type == "response.output_item.done") && event.Item.Type == "message" {
		for _, part := range event.Item.Content {
			if part.Type == "output_text" && part.Text != "" {
				visibleText = true
			}
		}
	}
	if reasoning {
		Stage(ctx, "first_reasoning")
	}
	if visibleText {
		Stage(ctx, "first_visible_text")
	}
}

type SocketEvidence struct {
	MinRTTMS        *float64 `json:"min_rtt_ms"`
	Retransmissions *uint64  `json:"retransmissions"`
	Status          string   `json:"status"`
	Source          string   `json:"source"`
	Scope           string   `json:"scope"`
}
type ContentObservation struct {
	SSEEventFirstMS              *float64       `json:"sse_event_first_ms"`
	SemanticFirstMS              *float64       `json:"semantic_first_ms"`
	ReasoningFirstMS             *float64       `json:"reasoning_first_ms"`
	VisibleTextFirstMS           *float64       `json:"visible_text_first_ms"`
	ResponseByteFirstMS          *float64       `json:"response_byte_first_ms"`
	Status                       string         `json:"status"`
	Source                       string         `json:"source"`
	Scope                        string         `json:"scope"`
	ReasoningStreamingCapability string         `json:"reasoning_streaming_capability"`
	CapabilitySource             string         `json:"capability_source"`
	ClientSocket                 SocketEvidence `json:"client_socket"`
	UpstreamSocket               SocketEvidence `json:"upstream_socket"`
}

func UnknownContentObservation() ContentObservation {
	return ContentObservation{Status: "not_collected", Source: "none", Scope: "final_physical_http_attempt_offsets_from_application_entry", ReasoningStreamingCapability: "unknown", CapabilitySource: "none",
		ClientSocket:   SocketEvidence{Status: "not_collected_or_reverse_proxy_obscured", Source: "none", Scope: "client_to_server; application_direct_peer_may_be_reverse_proxy"},
		UpstreamSocket: SocketEvidence{Status: "not_collected", Source: "none", Scope: "server_to_upstream"}}
}
func DeriveContentObservation(s Snapshot) ContentObservation {
	out := UnknownContentObservation()
	if s.Truncated || len(s.Attempts) == 0 {
		return out
	}
	a, ok := finalContentAttempt(s)
	if !ok {
		return out
	}
	plugin := a.Kind == "egress" && a.Route.Mode == "plugin" && a.Route.Upstream == "plugin"
	websocket := a.Kind == "egress" && a.Route.Upstream == "websocket"
	if a.Kind != "http" && !plugin && !websocket {
		return out
	}
	out.Status = "partial"
	out.Source = "httptrace_and_typed_sse"
	if plugin {
		out.Source = "host_plugin_body_parser"
		out.Scope = "final_plugin_egress_attempt_offsets_from_application_entry"
		out.UpstreamSocket.Status = "plugin_transport_not_observed"
	} else if websocket {
		out.Source = "native_websocket_events"
		out.Scope = "final_websocket_egress_attempt_offsets_from_application_entry"
		out.UpstreamSocket.Status = "websocket_transport_not_observed"
	} else if a.ProxyID > 0 {
		out.UpstreamSocket.Status = "proxy_obscured"
		out.UpstreamSocket.Scope = "server_to_configured_proxy; upstream_origin_socket_not_observed"
	}
	read := func(key string) *float64 {
		v, ok := a.Events[key]
		if a.EndMS == nil || !finite(*a.EndMS) {
			return nil
		}
		upper := *a.EndMS
		if !websocket && key != "first_byte" && finite(s.TotalMS) && s.TotalMS > upper {
			upper = s.TotalMS
		}
		if !ok || !finite(v) || !finite(a.StartMS) || a.StartMS < 0 || v < a.StartMS || v > upper {
			return nil
		}
		return &v
	}
	out.ReasoningFirstMS = read("first_reasoning")
	out.VisibleTextFirstMS = read("first_visible_text")
	out.SSEEventFirstMS = read("first_sse_event_flush")
	out.SemanticFirstMS = read("first_semantic")
	if !plugin && !websocket {
		out.ResponseByteFirstMS = read("first_byte")
	}
	if out.ReasoningFirstMS != nil || out.VisibleTextFirstMS != nil || out.ResponseByteFirstMS != nil || out.SSEEventFirstMS != nil || out.SemanticFirstMS != nil {
		out.Status = "observed"
	}
	return out
}

// New snapshots identify the logical response owner explicitly. Physical HTTP
// retries are children of that owner; a native WS handshake is not the response.
// Historical snapshots keep their original last-attempt projection.
func finalContentAttempt(s Snapshot) (Attempt, bool) {
	if s.FinalResponseAttempt == 0 {
		return s.Attempts[len(s.Attempts)-1], true
	}
	root := s.FinalResponseAttempt - 1
	if root < 0 || root >= len(s.Attempts) || s.Attempts[root].Number != s.FinalResponseAttempt || s.Attempts[root].Parent != 0 {
		return Attempt{}, false
	}
	a := s.Attempts[root]
	if a.Route.Upstream == "websocket" || (a.Route.Upstream == "plugin" && a.Route.Mode == "plugin") {
		return a, true
	}
	for i := len(s.Attempts) - 1; i > root; i-- {
		if s.Attempts[i].Kind != "http" {
			continue
		}
		for parent := s.Attempts[i].Parent; parent > 0; {
			if parent == s.FinalResponseAttempt {
				return s.Attempts[i], true
			}
			index := parent - 1
			// Parents must precede children. Malformed historical JSON cannot
			// create a cycle or borrow a different attempt's content evidence.
			if index < 0 || index >= i || index >= len(s.Attempts) || s.Attempts[index].Parent >= parent {
				break
			}
			parent = s.Attempts[index].Parent
		}
	}
	return a, true
}

func NormalizeObservationEffort(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > 32 {
		return ""
	}
	return value
}
