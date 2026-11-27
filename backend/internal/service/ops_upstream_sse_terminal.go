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

func (terminal *opsUpstreamSSETerminal) complete() {
	terminal.confirmed = terminal.pending
	terminal.pending = false
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
