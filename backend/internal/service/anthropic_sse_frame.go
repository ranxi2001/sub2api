package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

var errAnthropicSSEFrameTooLarge = errors.New("upstream SSE frame exceeds size limit")

// SSE accepts LF, CRLF and lone CR; a CR at a network boundary must wait for
// the following byte so its LF is not mistaken for an empty frame. The BOM is
// ignored only at the beginning of the stream.
func anthropicSSELineSplit() bufio.SplitFunc {
	first := true
	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		for i, b := range data {
			if b != '\r' && b != '\n' {
				continue
			}
			if b == '\r' && i+1 == len(data) && !atEOF {
				return 0, nil, nil
			}
			advance, token = i+1, data[:i]
			if b == '\r' && i+1 < len(data) && data[i+1] == '\n' {
				advance++
			}
			break
		}
		if advance == 0 && atEOF && len(data) > 0 {
			advance, token = len(data), data
		}
		if first && advance > 0 {
			token = bytes.TrimPrefix(token, []byte("\xef\xbb\xbf"))
			first = false
		}
		return
	}
}

// anthropicSSEFrameAssembler bounds the entire frame, including ignored fields.
// Scanner/line-pump boundaries are transport details, never event boundaries.
// Metadata is not interpreted as JSON. Data fields retain whitespace other than
// the single optional space after ':', as required by SSE.
type anthropicSSEFrameAssembler struct {
	name    string
	data    strings.Builder
	bytes   int
	hasData bool
}

func (a *anthropicSSEFrameAssembler) line(line string, limit int) (name, payload string, ready bool, err error) {
	if line == "" {
		name, payload, ready = a.name, a.data.String(), a.hasData
		*a = anthropicSSEFrameAssembler{}
		return
	}
	if limit <= 0 {
		limit = defaultMaxLineSize
	}
	if len(line)+1 > limit-a.bytes {
		return "", "", false, errAnthropicSSEFrameTooLarge
	}
	a.bytes += len(line) + 1
	field, value, _ := strings.Cut(line, ":")
	value = strings.TrimPrefix(value, " ")
	switch field {
	case "event":
		a.name = value
	case "data":
		if a.hasData {
			_ = a.data.WriteByte('\n')
		}
		_, _ = a.data.WriteString(value)
		a.hasData = true
	}
	return
}

func knownAnthropicStreamEvent(kind string) bool {
	switch kind {
	case "message_start", "message_delta", "message_stop", "content_block_start", "content_block_delta", "content_block_stop", "ping", "error":
		return true
	}
	return false
}

// nextEvent is shared by streaming and buffered Anthropic adapters. A final
// complete JSON frame without a blank line is accepted only on clean EOF, never
// on a transport error. [DONE] alone is not an Anthropic terminal.
func (st *anthropicCompatStream) nextEvent() (apicompat.AnthropicStreamEvent, string, bool) {
	for st.readErr == nil && st.protocolFailure == "" {
		line, ok := st.nextLine()
		if !ok && !errors.Is(st.readErr, io.EOF) {
			break
		}
		name, payload, ready, err := st.frame.line(line, st.maxLineSize)
		if err != nil {
			st.readErr = err
			_ = st.body.Close()
			break
		}
		if !ready { // SSE does not dispatch an event without a data field.
			continue
		}
		if event, valid := st.decodeFrame(name, payload); valid {
			return event, payload, true
		}
	}
	return apicompat.AnthropicStreamEvent{}, "", false
}

// decodeFrame is also used by native relays that keep their keepalive timers.
func (st *anthropicCompatStream) decodeFrame(name, payload string) (apicompat.AnthropicStreamEvent, bool) {
	st.frameCount++
	var event apicompat.AnthropicStreamEvent
	if strings.TrimSpace(payload) == "[DONE]" {
		return event, false
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		if name == "" || knownAnthropicStreamEvent(name) {
			st.protocolFailure = "invalid Anthropic event JSON"
		}
		return event, false
	}
	if event.Type == "" || (name != "" && name != "message" && name != event.Type) {
		st.protocolFailure = "Anthropic event name and JSON type disagree"
		return event, false
	}
	return event, knownAnthropicStreamEvent(event.Type)
}
