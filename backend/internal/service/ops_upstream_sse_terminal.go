package service

import (
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const opsUpstreamSSETerminalKey = "ops_upstream_sse_terminal"

// This evidence belongs to one upstream reader, not to the request's attempt
// history. Only a complete forwarded response.failed can confirm it.
type opsUpstreamSSETerminal struct {
	pending   bool
	buffered  bool
	confirmed bool
}

func beginOpsUpstreamSSETerminal(c *gin.Context) *opsUpstreamSSETerminal {
	terminal := &opsUpstreamSSETerminal{}
	if c != nil {
		c.Set(opsUpstreamSSETerminalKey, terminal)
	}
	return terminal
}

func (terminal *opsUpstreamSSETerminal) observe(payload []byte, eventType string) {
	terminal.pending = eventType == "response.failed" && gjson.ValidBytes(payload) &&
		gjson.GetBytes(payload, "response.error").IsObject()
}

// boundary means the complete event has entered the downstream buffer. A
// queued native reader may not flush here; only flushed can confirm delivery.
func (terminal *opsUpstreamSSETerminal) boundary() {
	terminal.buffered = terminal.pending
	terminal.pending = false
}

func (terminal *opsUpstreamSSETerminal) flushed() {
	if terminal.buffered {
		terminal.confirmed = true
		terminal.buffered = false
	}
}

func (terminal *opsUpstreamSSETerminal) discard() {
	terminal.pending = false
	terminal.buffered = false
	terminal.confirmed = false
}

// HasOpsUpstreamSSETerminal reports internal forwarding evidence. Neither an
// error code nor captured client-facing SSE text can establish this provenance.
func HasOpsUpstreamSSETerminal(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, _ := c.Get(opsUpstreamSSETerminalKey)
	terminal, _ := value.(*opsUpstreamSSETerminal)
	return terminal != nil && terminal.confirmed
}
