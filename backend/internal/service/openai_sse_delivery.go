package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
)

// Observe only complete data events, independently of semantic TTFT and of
// heartbeat flushes. Buffered complete events retain their actual flush time.
type openAISSEDeliveryObservation struct {
	eventData bool
	complete  bool
}

func (o *openAISSEDeliveryObservation) data(payload []byte, eventType string) {
	if len(payload) == 0 || string(payload) == "[DONE]" {
		return
	}
	switch strings.TrimSpace(eventType) {
	case "keepalive", "heartbeat", "ping", "pong":
		return
	}
	o.eventData = true
}

func (o *openAISSEDeliveryObservation) boundary() {
	o.complete = o.complete || o.eventData
	o.eventData = false
}

func (o *openAISSEDeliveryObservation) flushed(ctx context.Context) {
	if o.complete {
		requesttiming.SSEEventFlushed(ctx)
		o.complete = false
	}
}

// discard abandons the current incomplete or suppressed event without losing
// a previously buffered complete event that has yet to be flushed.
func (o *openAISSEDeliveryObservation) discard() {
	o.eventData = false
}
