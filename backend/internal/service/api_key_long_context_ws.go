package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/google/uuid"
)

// WriteAPIKeyLongContextWSFailure sends a standard Responses terminal event
// before either a relay or its handler closes the socket. Both layers can see
// the same error, so emit it once. A bare close loses the machine-readable code.
func WriteAPIKeyLongContextWSFailure(conn *coderws.Conn, err error) {
	var policyErr *APIKeyLongContextError
	if conn == nil || !errors.As(err, &policyErr) || !policyErr.wsFailureSent.CompareAndSwap(false, true) {
		return
	}
	payload, marshalErr := json.Marshal(map[string]any{
		"type": "response.failed", "sequence_number": 0,
		"response": map[string]any{
			"id": "resp_" + uuid.NewString(), "object": "response",
			"created_at": time.Now().Unix(), "status": "failed", "output": []any{},
			"error": map[string]any{"code": APIKeyContextLengthExceededCode, "message": APIKeyContextLengthExceededMessage},
		},
	})
	if marshalErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = conn.Write(ctx, coderws.MessageText, payload)
	}
}
