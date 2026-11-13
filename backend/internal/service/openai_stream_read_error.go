package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/http2"
)

const (
	// OpenAIUpstreamHTTP2StreamErrorCode is returned to OpenAI-compatible clients
	// when an upstream HTTP/2 response stream is reset after the request started.
	OpenAIUpstreamHTTP2StreamErrorCode = "upstream_http2_stream_error"
	OpenAIUpstreamStreamReadErrorCode  = "upstream_stream_read_error"
	// OpenAIUpstreamStreamTruncatedCode is returned when an upstream SSE stream
	// closes *cleanly* before delivering any terminal signal. A clean EOF carries
	// no transport error, so without this classification a truncated generation is
	// indistinguishable from a successful one.
	OpenAIUpstreamStreamTruncatedCode = "upstream_stream_truncated"
)

// ErrOpenAIUpstreamStreamTruncated marks an upstream SSE stream that ended at
// EOF — without a read error — before any terminal signal arrived.
var ErrOpenAIUpstreamStreamTruncated = errors.New("upstream stream ended before any terminal chunk")

type openAIUpstreamStreamReadError struct {
	cause         error
	clientCode    string
	clientMessage string
}

func (e *openAIUpstreamStreamReadError) Error() string {
	return fmt.Sprintf("stream usage incomplete: %v", e.cause)
}

func (e *openAIUpstreamStreamReadError) Unwrap() error { return e.cause }

func newOpenAIUpstreamStreamReadError(err error) error {
	code, message := classifyOpenAIUpstreamStreamReadError(err)
	return &openAIUpstreamStreamReadError{
		cause:         err,
		clientCode:    code,
		clientMessage: message,
	}
}

// shouldClassifyOpenAIUpstreamStreamReadError excludes cancellation and
// response-size enforcement from upstream retry.
func shouldClassifyOpenAIUpstreamStreamReadError(err error, contexts ...context.Context) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
		return false
	}
	for _, ctx := range contexts {
		if ctx != nil && ctx.Err() != nil {
			return false
		}
	}
	return true
}

// OpenAIUpstreamStreamReadErrorDetails returns the stable, sanitized client
// classification attached to an upstream stream read failure.
func OpenAIUpstreamStreamReadErrorDetails(err error) (code, message string, ok bool) {
	var streamErr *openAIUpstreamStreamReadError
	if !errors.As(err, &streamErr) || streamErr == nil {
		return "", "", false
	}
	return streamErr.clientCode, streamErr.clientMessage, true
}

func classifyOpenAIUpstreamStreamReadError(err error) (code, message string) {
	if reason, ok := openAIHTTP2PeerResetReason(err); ok {
		return OpenAIUpstreamHTTP2StreamErrorCode, openAIHTTP2PeerResetMessage(reason)
	}
	if err != nil {
		if errors.Is(err, ErrOpenAIUpstreamStreamTruncated) {
			return OpenAIUpstreamStreamTruncatedCode, "Upstream response stream ended before completion"
		}
		lower := strings.ToLower(err.Error())
		// net/http's HTTP/2 stream error is unexported. Its stable text contains
		// "stream error: stream ID ..."; match only the transport signature and
		// never pass the original text to the client.
		if strings.Contains(lower, "stream error: stream id ") ||
			(strings.Contains(lower, "http2:") && strings.Contains(lower, "stream")) {
			return OpenAIUpstreamHTTP2StreamErrorCode, "Upstream HTTP/2 stream failed"
		}
	}
	return OpenAIUpstreamStreamReadErrorCode, "Upstream response stream was interrupted"
}

// net/http uses an unexported copy of http2.StreamError. Match its complete
// peer-reset signature, not an arbitrary mention of HTTP/2 in an error body.
var openAIHTTP2PeerResetPattern = regexp.MustCompile(`(?:^|: )stream error: stream ID [0-9]+; ([^;\r\n]+); received from peer$`)

func safeHTTP2ResetReason(reason string) string {
	switch reason {
	case "NO_ERROR", "PROTOCOL_ERROR", "INTERNAL_ERROR", "FLOW_CONTROL_ERROR", "SETTINGS_TIMEOUT", "STREAM_CLOSED", "FRAME_SIZE_ERROR", "REFUSED_STREAM", "CANCEL", "COMPRESSION_ERROR", "CONNECT_ERROR", "ENHANCE_YOUR_CALM", "INADEQUATE_SECURITY", "HTTP_1_1_REQUIRED":
		return reason
	default:
		return ""
	}
}

func openAIHTTP2PeerResetReason(err error) (string, bool) {
	if !shouldClassifyOpenAIUpstreamStreamReadError(err) {
		return "", false
	}
	var streamErr http2.StreamError
	if errors.As(err, &streamErr) && streamErr.Cause != nil && streamErr.Cause.Error() == "received from peer" {
		return safeHTTP2ResetReason(streamErr.Code.String()), true
	}
	match := openAIHTTP2PeerResetPattern.FindStringSubmatch(err.Error())
	if len(match) != 2 {
		return "", false
	}
	return safeHTTP2ResetReason(match[1]), true
}

func openAIHTTP2PeerResetMessage(reason string) string {
	protocol := "HTTP/2"
	if reason = safeHTTP2ResetReason(reason); reason != "" {
		protocol += " " + reason
	}
	return "Upstream connection reset by peer (" + protocol + "). Content may be incomplete. Do not automatically retry the same request."
}

// OpsTransportFailure is trusted local evidence, never inferred from SSE text.
// Keeping it separate from HTTP status avoids both false success and 502 policy.
type OpsTransportFailure struct {
	ResetReason string `json:"reset_reason,omitempty"`
}

func (f *OpsTransportFailure) ErrorFields() map[string]any {
	return map[string]any{"type": "upstream_error", "code": OpenAIUpstreamHTTP2StreamErrorCode,
		"category": "transport_error", "protocol": "HTTP/2", "reset_reason": safeHTTP2ResetReason(f.ResetReason),
		"message": openAIHTTP2PeerResetMessage(f.ResetReason)}
}

// handleOpenAIHTTP2PeerReset ends this submitted request at the read boundary.
// The caller must discard any incomplete staged event before invoking it. A
// typed read error (not UpstreamFailoverError) prevents account or request replay.
func handleOpenAIHTTP2PeerReset(c *gin.Context, account *Account, requestID string, cause error) (error, bool) {
	if c == nil || c.Request == nil || !shouldClassifyOpenAIUpstreamStreamReadError(cause, c.Request.Context()) {
		return nil, false
	}
	reason, ok := openAIHTTP2PeerResetReason(cause)
	if !ok {
		return nil, false
	}
	classified := newOpenAIUpstreamStreamReadError(cause)
	if marked, exists := GetOpsStreamError(c); exists && marked.TransportFailure != nil {
		return classified, true
	}
	StopOpenAICompactSSEKeepaliveCommitted(c)
	StopOpenAIImagesJSONKeepaliveCommitted(c)
	failure := &OpsTransportFailure{ResetReason: reason}
	message := openAIHTTP2PeerResetMessage(reason)
	c.Set(OpsUpstreamStatusCodeKey, 0)
	c.Set(OpsUpstreamErrorMessageKey, message)
	c.Set(OpsUpstreamErrorDetailKey, "")
	event := OpsUpstreamErrorEvent{Kind: "transport_error", Stage: "response_body", Reason: reason,
		Message: message, UpstreamRequestID: requestID, ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account)}
	if account != nil {
		event.Platform, event.AccountID, event.AccountName = account.Platform, account.ID, account.Name
	}
	appendOpsUpstreamError(c, event)
	MarkOpsStreamErrorValue(c, OpsStreamError{TransportFailure: failure, ErrType: "upstream_error",
		Code: OpenAIUpstreamHTTP2StreamErrorCode, Message: message, IntendedStatus: http.StatusBadGateway, CountTowardsSLA: true})
	fields := failure.ErrorFields()
	// HTTP bridge WebSocket callers own their frame writer; don't write HTTP SSE
	// to an upgraded connection. Their caller consumes the typed safe details.
	if GetOpenAIClientTransport(c) == OpenAIClientTransportWS {
		return classified, true
	}
	if c.Writer.Written() && strings.Contains(c.Writer.Header().Get("Content-Type"), "text/event-stream") {
		eventType := "error"
		payload := map[string]any{"error": fields}
		if strings.HasSuffix(strings.TrimRight(c.Request.URL.Path, "/"), "/responses") {
			eventType = "response.failed"
			payload = map[string]any{"type": eventType, "sequence_number": 0, "response": map[string]any{"status": "failed", "error": fields}}
		} else if strings.HasSuffix(c.Request.URL.Path, "/messages") {
			payload["type"] = "error"
		}
		data, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(c.Writer, "\n\nevent: %s\ndata: %s\n\n", eventType, data)
		c.Writer.Flush()
	} else {
		status := http.StatusBadGateway
		if c.Writer.Written() {
			status = c.Writer.Status()
		}
		c.JSON(status, gin.H{"error": fields})
	}
	MarkResponseCommitted(c)
	return classified, true
}
