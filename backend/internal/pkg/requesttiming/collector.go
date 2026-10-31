// Package requesttiming records bounded, request-local observations. It never
// retains request/response bodies, headers, URLs, or raw error messages.
package requesttiming

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/transportdiag"
	"github.com/google/uuid"
)

type key struct{}
type Span struct {
	Name    string  `json:"name"`
	StartMS float64 `json:"start_ms"`
	EndMS   float64 `json:"end_ms"`
	Attempt int     `json:"attempt,omitempty"`
}
type Attempt struct {
	ContentEncoding  string              `json:"content_encoding,omitempty"`
	AutoDecompressed bool                `json:"auto_decompressed,omitempty"`
	Route            transportdiag.Route `json:"route"`
	Terminal         string              `json:"terminal,omitempty"`
	CleanupCanceled  bool                `json:"cleanup_canceled,omitempty"`
	Kind             string              `json:"kind"`
	Parent           int                 `json:"parent,omitempty"`
	Number           int                 `json:"number"`
	AccountID        int64               `json:"account_id"`
	ProxyID          int64               `json:"proxy_id"`
	StartMS          float64             `json:"start_ms"`
	EndMS            *float64            `json:"end_ms"`
	Status           int                 `json:"status"`
	Error            string              `json:"error,omitempty"`
	Reused           *bool               `json:"reused"`
	RequestBytes     int64               `json:"request_bytes"`
	ResponseBytes    int64               `json:"response_bytes"`
	BodyEOF          bool                `json:"body_eof"`
	Events           map[string]float64  `json:"events"`
}
type Snapshot struct {
	WSTurns           []WSTurnTiming     `json:"ws_turns,omitempty"`
	ProtocolDecision  *ProtocolDecision  `json:"protocol_decision,omitempty"`
	Latency           *LatencySummary    `json:"latency,omitempty"`
	IdentitySource    string             `json:"identity_source,omitempty"`
	AuthRejection     string             `json:"auth_rejection,omitempty"`
	Routes            []RouteObservation `json:"routes,omitempty"`
	Outcome           string             `json:"outcome,omitempty"`
	ClientDisconnect  bool               `json:"client_disconnect"`
	Version           int                `json:"version"`
	TraceID           string             `json:"trace_id"`
	StartedAt         time.Time          `json:"started_at"`
	TotalMS           float64            `json:"total_ms"`
	Status            int                `json:"status"`
	Canceled          bool               `json:"canceled"`
	Truncated         bool               `json:"truncated"`
	BodyBytes         int64              `json:"body_bytes"`
	BodyExpected      int64              `json:"body_expected"`
	BodyComplete      bool               `json:"body_complete"`
	BodyReadMS        float64            `json:"body_read_ms"`
	DownstreamBytes   int64              `json:"downstream_bytes"`
	DownstreamWriteMS float64            `json:"downstream_write_ms"`
	DownstreamError   bool               `json:"downstream_error"`
	TTFTMode          string             `json:"ttft_mode,omitempty"`
	Terminal          string             `json:"terminal,omitempty"`
	TerminalFlushed   string             `json:"terminal_flushed,omitempty"`
	Events            map[string]float64 `json:"events"`
	Spans             []Span             `json:"spans"`
	Attempts          []Attempt          `json:"attempts"`
}
type Collector struct {
	mu        sync.Mutex
	start     time.Time
	now       func() time.Time
	data      Snapshot
	finished  bool
	callbacks []func(Snapshot)
}

func New(start time.Time, expected int64) *Collector {
	return newCollector(start, expected, time.Now)
}

func newCollector(start time.Time, expected int64, now func() time.Time) *Collector {
	return &Collector{start: start, now: now, data: Snapshot{Version: 1, TraceID: uuid.NewString(), StartedAt: start, BodyExpected: expected, Events: map[string]float64{}, Spans: []Span{}, Attempts: []Attempt{}}}
}
func With(ctx context.Context, c *Collector) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, key{}, c)
}
func From(ctx context.Context) *Collector {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(key{}).(*Collector)
	return c
}
func (c *Collector) offset(t time.Time) float64 {
	return float64(t.Sub(c.start)) / float64(time.Millisecond)
}
func (c *Collector) event(name string, t time.Time) {
	if _, ok := c.data.Events[name]; !ok {
		c.data.Events[name] = c.offset(t)
	}
}
func Mark(ctx context.Context, name string) {
	c := From(ctx)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.finished {
		c.event(name, c.now())
	}
}
func Observe(ctx context.Context, name string) func() {
	c := From(ctx)
	if c == nil {
		return func() {}
	}
	start := c.now()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.finished {
				return
			}
			if len(c.data.Spans) >= 256 {
				c.data.Truncated = true
				return
			}
			c.data.Spans = append(c.data.Spans, Span{Name: name, StartMS: c.offset(start), EndMS: c.offset(c.now())})
		})
	}
}
func Output(ctx context.Context, semantic, visible bool, terminal string) {
	c := From(ctx)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return
	}
	now := c.now()
	if tr, ok := ctx.Value(attemptKey{}).(*Trace); ok && tr.c == c {
		for index := tr.index; index >= 0; {
			a := &c.data.Attempts[index]
			if semantic {
				if _, exists := a.Events["first_semantic"]; !exists {
					a.Events["first_semantic"] = c.offset(now)
				}
			}
			if visible {
				if _, exists := a.Events["first_visible"]; !exists {
					a.Events["first_visible"] = c.offset(now)
				}
			}
			if terminal != "" {
				a.Events["terminal"] = c.offset(now)
				a.Terminal = terminal
			}
			index = a.Parent - 1
		}
	}
	if semantic {
		c.event("first_semantic", now)
	}
	if visible {
		c.event("first_visible", now)
	}
	if terminal != "" {
		c.data.Terminal = terminal
		c.event("terminal", now)
	}
}
func Mode(ctx context.Context, mode string) {
	c := From(ctx)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.finished {
		c.data.TTFTMode = mode
	}
}
func (c *Collector) snapshot() Snapshot {
	d := c.data
	if d.ProtocolDecision != nil {
		decision := *d.ProtocolDecision
		d.ProtocolDecision = &decision
	}
	d.WSTurns = append([]WSTurnTiming(nil), d.WSTurns...)
	for i := range d.WSTurns {
		d.WSTurns[i] = cloneTurnTiming(d.WSTurns[i])
	}
	d.Routes = append([]RouteObservation(nil), d.Routes...)
	d.Events = cloneEvents(d.Events)
	d.Spans = append([]Span{}, d.Spans...)
	d.Attempts = append([]Attempt{}, d.Attempts...)
	for i := range d.Attempts {
		d.Attempts[i].Events = cloneEvents(d.Attempts[i].Events)
	}
	latency := DeriveLatency(d)
	d.Latency = &latency
	return d
}
func cloneEvents(src map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// WhenFinished works in either order: the billing worker can bind a persisted
// usage record before or after the HTTP middleware has completed.
func (c *Collector) WhenFinished(fn func(Snapshot)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.finished {
		if len(c.callbacks) < 8 {
			c.callbacks = append(c.callbacks, fn)
		} else {
			c.data.Truncated = true
		}
		c.mu.Unlock()
		return
	}
	d := c.snapshot()
	c.mu.Unlock()
	fn(d)
}
func (c *Collector) Finish(status int, canceled bool) {
	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return
	}
	c.finished = true
	c.data.TotalMS = c.offset(c.now())
	c.data.Status = status
	c.data.Canceled = canceled
	callbacks := c.callbacks
	c.callbacks = nil
	d := c.snapshot()
	c.mu.Unlock()
	for _, fn := range callbacks {
		fn(d)
	}
}

func Outcome(ctx context.Context, result string, disconnected bool) {
	c := From(ctx)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.finished {
		c.data.Outcome = result
		c.data.ClientDisconnect = disconnected
	}
}
