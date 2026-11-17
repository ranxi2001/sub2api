package service

import (
	"errors"
	"net/http"
	"strings"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Only the caller at read_upstream may set this origin. The peer's reason is
// untrusted and may contain task text or secrets; retain only a known code.
// A close frame proves neither non-execution nor safe replay of this turn.
func markOpenAIWSUpstreamClose(c *gin.Context, err error) {
	var closeErr coderws.CloseError
	if !errors.As(err, &closeErr) {
		return
	}
	code, message, status := "upstream_websocket_closed", "Upstream closed the WebSocket before the current turn completed; execution state is unknown. Preserve the task and context.", http.StatusBadGateway
	const ownerUnavailable = "encrypted_agent_recovery_owner_unavailable"
	reason := strings.TrimSpace(closeErr.Reason)
	if reason == ownerUnavailable || strings.HasPrefix(reason, ownerUnavailable+":") ||
		gjson.Get(reason, "error.code").String() == ownerUnavailable {
		code, status = ownerUnavailable, http.StatusConflict
		message = "Upstream reported that the encrypted agent owner is unavailable. Preserve the task and context; the current turn was not replayed."
	}
	MarkOpsStreamErrorValue(c, OpsStreamError{
		ErrType: "upstream_error", Code: code, Message: message,
		IntendedStatus: status, CountTowardsSLA: true,
		UpstreamWSFailure: &OpsUpstreamWSFailure{Phase: "read_upstream", CloseCode: int(closeErr.Code)},
	})
}
