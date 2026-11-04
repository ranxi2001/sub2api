package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// openAISSEMetricEvent observes payloads without changing delivery lines,
// flushing, usage parsing or replay eligibility. Single-line JSON is returned
// immediately; multiline JSON is returned when its final data line arrives.
type openAISSEMetricEvent struct {
	eventType string
	data      []byte
	discard   bool
	limit     int
}

func (e *openAISSEMetricEvent) observeLine(line string) ([]byte, string) {
	if line == "" || line == "event" {
		e.reset()
		return nil, ""
	}
	if eventType, ok := extractOpenAISSEEventLine(line); ok {
		e.reset()
		e.eventType = eventType
		return nil, ""
	}
	data, ok := extractOpenAISSEDataLine(line)
	if !ok || e.discard {
		return nil, ""
	}
	limit := e.limit
	if limit <= 0 {
		limit = maxOpenAIConcatenatedJSONBytes
	}
	separator := 0
	if len(e.data) > 0 {
		separator = 1
	}
	if len(data) > limit-len(e.data)-separator {
		e.data = nil
		e.discard = true
		return nil, ""
	}
	if separator > 0 {
		e.data = append(e.data, '\n')
	}
	e.data = append(e.data, data...)
	if strings.TrimSpace(string(e.data)) == "[DONE]" {
		e.data = nil
		return []byte("[DONE]"), ""
	}
	if !gjson.ValidBytes(e.data) {
		return nil, ""
	}
	payload := e.data
	e.data = nil
	return payload, effectiveOpenAISSEEventType(payload, e.eventType)
}

func (e *openAISSEMetricEvent) reset() {
	e.eventType = ""
	e.data = nil
	e.discard = false
}
