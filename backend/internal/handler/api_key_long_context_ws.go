package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
)

// WebSocket close reasons must fit in the protocol's 123-byte limit.
func apiKeyLongContextWSCloseReason(err error) string {
	return service.APIKeyContextLengthExceededMessage
}

// A bare policy close looks like a disconnected stream to Responses clients.
// Send the same terminal event as HTTP streaming before closing the socket.
// Call only before forwarding starts or after the forwarding loop has returned.
func rejectAPIKeyLongContextWS(conn *coderws.Conn, err error) {
	if conn == nil {
		return
	}
	service.WriteAPIKeyLongContextWSFailure(conn, err)
	closeOpenAIClientWS(conn, coderws.StatusPolicyViolation, apiKeyLongContextWSCloseReason(err))
}
