package basispoints

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

// SearchFailure contains only bounded diagnostics, never the underlying error.
// Its HTTP status is observed evidence, independent of the semantic 502 status.
type SearchFailure struct {
	phase          string
	reason         string
	upstreamStatus int
}

// NewSearchFailure is used at adapter boundaries with an actually observed HTTP
// status, or zero when no response was received. Unknown identifiers are dropped.
func NewSearchFailure(phase, reason string, upstreamStatus int) *SearchFailure {
	known := false
	switch phase {
	case "search_http":
		switch reason {
		case "request_failed", "transport_failed", "http_rejected", "deadline_exceeded", "canceled":
			known = true
		}
	case "search_result":
		known = reason == "read_failed" || reason == "parse_failed"
	case "model_initial", "model_continuation":
		switch reason {
		case "request_failed", "transport_failed", "http_rejected", "read_failed", "unexpected_eof",
			"invalid_event", "model_did_not_complete", "missing_response_id", "invalid_continuation",
			"continuation_too_large", "missing_continuation", "deadline_exceeded", "canceled",
			"invalid_output_index", "too_many_tool_items", "missing_item_id", "item_identity_changed",
			"invalid_text_delta", "text_too_large", "missing_terminal_tool", "missing_terminal_item",
			"terminal_identity_changed", "missing_terminal_text", "terminal_text_changed":
			known = true
		}
	case "tool_validation":
		switch reason {
		case "invalid_tool_response", "duplicate_item_id", "duplicate_call_id", "invalid_query",
			"parallel_search_unavailable", "mixed_tool_batch", "required_search_missing",
			"required_tool_missing", "executor_unavailable":
			known = true
		}
	default:
		phase = "unknown"
	}
	if !known {
		reason = "unknown"
	}
	if upstreamStatus < 100 || upstreamStatus > 599 {
		upstreamStatus = 0
	}
	return &SearchFailure{phase: phase, reason: reason, upstreamStatus: upstreamStatus}
}

func (f *SearchFailure) Error() string {
	message := "phase=" + f.phase + "; reason=" + f.reason
	if f.upstreamStatus != 0 {
		message += "; upstream_status=" + strconv.Itoa(f.upstreamStatus)
	}
	return message
}

func (f *SearchFailure) details() object {
	detail := object{"phase": f.phase, "reason": f.reason}
	if f.upstreamStatus != 0 {
		detail["upstream_status"] = f.upstreamStatus
	}
	return detail
}

func searchFailureAt(err error, phase, fallback string) error {
	// Preserve the existing closed-pipe exit without synthesizing a terminal.
	if errors.Is(err, io.ErrClosedPipe) {
		return err
	}
	var failure *SearchFailure
	if errors.As(err, &failure) {
		return NewSearchFailure(failure.phase, failure.reason, failure.upstreamStatus)
	}
	var rejection searchRequestRejection
	switch {
	case errors.As(err, &rejection):
		fallback = string(rejection)
	case errors.Is(err, context.DeadlineExceeded):
		fallback = "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		fallback = "canceled"
	case errors.Is(err, io.ErrUnexpectedEOF):
		fallback = "unexpected_eof"
	}
	return NewSearchFailure(phase, fallback, 0)
}

// Revalidate at the SSE boundary; never trust arbitrary upstream diagnostics.
func parseSearchFailure(value any) *SearchFailure {
	detail, _ := value.(object)
	status := 0
	if number, ok := detail["upstream_status"].(json.Number); ok {
		if n, err := number.Int64(); err == nil && n >= 100 && n <= 599 {
			status = int(n)
		}
	}
	return NewSearchFailure(text(detail["phase"]), text(detail["reason"]), status)
}
