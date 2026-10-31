package requesttiming

import "context"

// ProtocolDecision is optional in existing timing JSON. Old rows remain absent.
// Values originate from server enums and bounded numeric aggregates only.
type ProtocolDecision struct {
	Ingress          string `json:"ingress"`
	Upstream         string `json:"upstream"`
	Candidate        string `json:"candidate"`
	Mode             string `json:"mode"`
	Reason           string `json:"reason"`
	PredictionSource string `json:"prediction_source"`
	PredictedOutput  int    `json:"predicted_output,omitempty"`
	Samples          int    `json:"samples"`
}

func SetProtocolDecision(ctx context.Context, d ProtocolDecision) {
	c := From(ctx)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.finished && c.data.ProtocolDecision == nil {
		c.data.ProtocolDecision = &d
	}
}

// WSTurnTiming separates arrival of any upstream event from actual content.
// Nil means not observed. The existing timing row bounds the session to 128 turns.
type WSTurnTiming struct {
	Turn                   int    `json:"turn"`
	Upstream               string `json:"upstream"`
	FirstUpstreamEventMS   *int   `json:"first_upstream_event_ms,omitempty"`
	FirstActualContentKind string `json:"first_actual_content_kind,omitempty"`
	FirstActualContentMS   *int   `json:"first_actual_content_ms,omitempty"`
	UsageAvailable         *bool  `json:"usage_available"`
}

func cloneTurnTiming(t WSTurnTiming) WSTurnTiming {
	if t.UsageAvailable != nil {
		v := *t.UsageAvailable
		t.UsageAvailable = &v
	}
	if t.FirstUpstreamEventMS != nil {
		v := *t.FirstUpstreamEventMS
		t.FirstUpstreamEventMS = &v
	}
	if t.FirstActualContentMS != nil {
		v := *t.FirstActualContentMS
		t.FirstActualContentMS = &v
	}
	return t
}
func RecordWSTurn(ctx context.Context, t WSTurnTiming) {
	c := From(ctx)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return
	}
	if len(c.data.WSTurns) >= 128 {
		c.data.Truncated = true
		return
	}
	c.data.WSTurns = append(c.data.WSTurns, cloneTurnTiming(t))
}
