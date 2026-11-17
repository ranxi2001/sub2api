package openai_ws_v2

import (
	"errors"
	"fmt"
	"strings"

	coderws "github.com/coder/websocket"
	"github.com/tidwall/gjson"
)

// CloseReasonForLog retains only a recognized protocol code. Peer close text
// can contain private task data, including after an otherwise known code.
func CloseReasonForLog(reason string) string {
	const ownerUnavailable = "encrypted_agent_recovery_owner_unavailable"
	reason = strings.TrimSpace(reason)
	if reason == ownerUnavailable || strings.HasPrefix(reason, ownerUnavailable+":") ||
		gjson.Get(reason, "error.code").String() == ownerUnavailable {
		return ownerUnavailable
	}
	return "upstream_websocket_closed"
}

// ErrorStringForLog sanitizes typed WebSocket closes at logging boundaries.
// The caller keeps the original error for routing and recovery cause checks;
// local errors without a peer CloseError retain their existing diagnostics.
func ErrorStringForLog(err error) string {
	if err == nil {
		return ""
	}
	var closeErr coderws.CloseError
	if errors.As(err, &closeErr) {
		return fmt.Sprintf("websocket closed: status=%d reason=%s", closeErr.Code, CloseReasonForLog(closeErr.Reason))
	}
	return err.Error()
}
