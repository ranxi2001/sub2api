package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	OpenAIWSSSEIncompleteCode    = "upstream_stream_incomplete"
	openAIWSSSEIncompleteMessage = "Upstream response stream ended before completion"
	openAIWSSSEMetadataMaxEvents = 8
	openAIWSSSEMetadataMaxBytes  = 64 << 10
)

// OpenAIWSSSEIncompleteError describes an already-submitted accelerated HTTP
// request. It is deliberately not a fallback/failover error: absent tokens do
// not establish that the provider did not execute the request or its tools.
type OpenAIWSSSEIncompleteError struct {
	Stage               string
	CloseCode           int
	EventCount          int
	TokenEventCount     int
	TerminalEventCount  int
	RequestWritten      bool
	DownstreamCommitted bool
	ResponseWritten     bool
	cause               error
	responseID          string
	model               string
	nextSequenceNumber  int64
}

func (*OpenAIWSSSEIncompleteError) Error() string {
	return OpenAIWSSSEIncompleteCode + ": " + openAIWSSSEIncompleteMessage
}

func (e *OpenAIWSSSEIncompleteError) Unwrap() error { return e.cause }

func isOpenAIWSSSEControlMetadata(eventType string) bool {
	return eventType == "codex.rate_limits" || eventType == "codex.response.metadata"
}

func (e *OpenAIWSSSEIncompleteError) detail() string {
	// Keep this schema independent from raw peer reason, event bodies, URLs and
	// identifiers. Even a caller-supplied Stage is normalized before persistence.
	stage := "awaiting_terminal"
	data, _ := json.Marshal(struct {
		Stage               string `json:"stage"`
		CloseCode           int    `json:"close_code"`
		EventCount          int    `json:"events"`
		TokenEventCount     int    `json:"token_events"`
		TerminalEventCount  int    `json:"terminal_events"`
		RequestWritten      bool   `json:"request_written"`
		DownstreamCommitted bool   `json:"downstream_committed"`
	}{stage, e.CloseCode, e.EventCount, e.TokenEventCount, e.TerminalEventCount, e.RequestWritten, e.DownstreamCommitted})
	return string(data)
}

func (s *OpenAIGatewayService) writeOpenAIWSSSEIncomplete(c *gin.Context, account *Account, incomplete *OpenAIWSSSEIncompleteError) bool {
	if c == nil || c.Writer == nil || incomplete == nil || incomplete.ResponseWritten {
		return false
	}
	if c.Request != nil && errors.Is(c.Request.Context().Err(), context.Canceled) {
		return false
	}
	detail := incomplete.detail()
	// The WebSocket upgraded successfully. 502 is the gateway's logical failure
	// status, not an upstream HTTP status returned by this stream.
	c.Set(OpsUpstreamStatusCodeKey, 0)
	setOpsUpstreamError(c, 0, openAIWSSSEIncompleteMessage, detail)
	if account != nil {
		proxyID, proxyName := opsUpstreamWSProxyAttribution(account)
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			ProxyID: proxyID, ProxyName: proxyName,
			Platform: account.Platform, AccountID: account.ID,
			Kind: "ws_stream_incomplete", Message: openAIWSSSEIncompleteMessage, Detail: detail,
		})
	}
	if !c.Writer.Written() {
		c.Header("Content-Type", "application/json")
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"type": "upstream_error", "code": OpenAIWSSSEIncompleteCode, "message": openAIWSSSEIncompleteMessage,
		}})
		incomplete.ResponseWritten = true
		// Request ownership must survive regional metering replacing the error.
		MarkResponseCommitted(c)
		return true
	}
	MarkOpsStreamFailure(c, "upstream_error", OpenAIWSSSEIncompleteCode, openAIWSSSEIncompleteMessage, http.StatusBadGateway)
	responseID := incomplete.responseID
	if responseID == "" {
		responseID = "resp_" + uuid.NewString()
	}
	source, _ := json.Marshal(gin.H{"sequence_number": incomplete.nextSequenceNumber, "error": gin.H{
		"type": "upstream_error", "code": OpenAIWSSSEIncompleteCode, "message": openAIWSSSEIncompleteMessage,
	}})
	payload := buildOpenAIWSHTTPBridgeFailedEvent(responseID, incomplete.model, time.Now().Unix(), source, openAIWSSSEIncompleteMessage)
	// An attempted terminal write after committed output must not cause a second
	// terminal frame from the handler; the client may receive only part of it.
	incomplete.ResponseWritten = true
	MarkResponseCommitted(c)
	if _, err := fmt.Fprintf(c.Writer, "event: response.failed\ndata: %s\n\n", payload); err != nil {
		_ = c.Error(err)
		return true
	}
	if err := FlushGatewayResponse(c.Writer); err != nil {
		_ = c.Error(err)
	}
	return true
}
