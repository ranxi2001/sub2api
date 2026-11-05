package handler

import (
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// WebSocket close reasons must fit in the protocol's 123-byte limit.
func apiKeyLongContextWSCloseReason(err error) string {
	var policyErr *service.APIKeyLongContextError
	if errors.As(err, &policyErr) && policyErr.Code == "context_length_exceeded" {
		return "long context is disabled for this API key; shorten the input"
	}
	return "long context is disabled for this API key; send complete visible text"
}
