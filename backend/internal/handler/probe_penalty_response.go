package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Keep text deltas streaming. Only terminal usage/success is held until the
// canonical ledger commits. Non-streaming bodies use the existing private spool.
type probePenaltyResponse struct {
	*imageBillingResponse
	pending  []byte
	tail     []byte
	live     bool
	holding  bool
	jsonMode bool
	seenBody bool
}

func newProbePenaltyResponse(w gin.ResponseWriter, stream bool) (*probePenaltyResponse, error) {
	spool, err := newImageBillingResponse(w, stream)
	if err != nil {
		return nil, err
	}
	return &probePenaltyResponse{imageBillingResponse: spool, jsonMode: !stream}, nil
}

func (w *probePenaltyResponse) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *probePenaltyResponse) Write(b []byte) (int, error) {
	if !w.seenBody && len(bytes.TrimSpace(b)) > 0 {
		first := bytes.TrimSpace(b)
		if first[0] != ':' {
			w.seenBody = true
			w.jsonMode = first[0] == '{' || !strings.Contains(w.header.Get("Content-Type"), "text/event-stream")
		}
	}
	if w.jsonMode {
		return w.imageBillingResponse.Write(b)
	}
	w.WriteHeaderNow()
	w.size += len(b)
	w.pending = append(w.pending, b...)
	for {
		end, separator := bytes.Index(w.pending, []byte("\n\n")), 2
		if crlf := bytes.Index(w.pending, []byte("\r\n\r\n")); crlf >= 0 && (end < 0 || crlf < end) {
			end, separator = crlf, 4
		}
		if end < 0 {
			break
		}
		block := w.pending[:end+separator]
		payload := probeEventData(block)
		kind := gjson.GetBytes(payload, "type").String()
		if kind == "message_delta" || kind == "message_stop" {
			w.holding = true
		}
		if w.holding {
			w.tail = append(w.tail, block...)
		} else {
			if !w.live {
				w.commitHeaders()
				w.live = true
			}
			if _, err := w.ResponseWriter.Write(block); err != nil {
				w.failure = err
				return 0, err
			}
		}
		w.pending = w.pending[end+separator:]
	}
	if len(w.pending)+len(w.tail) > 1<<20 {
		w.failure = errors.New("probe penalty terminal response exceeds 1 MiB")
		return 0, w.failure
	}
	return len(b), nil
}
func (w *probePenaltyResponse) Flush() {
	if w.live {
		w.ResponseWriter.Flush()
	}
}

func probeEventData(block []byte) []byte {
	var lines [][]byte
	for _, line := range bytes.Split(block, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			lines = append(lines, bytes.TrimPrefix(data, []byte(" ")))
		}
	}
	return bytes.Join(lines, []byte("\n"))
}

func (w *probePenaltyResponse) body() ([]byte, error) {
	if w.failure != nil {
		return nil, w.failure
	}
	if w.jsonMode {
		if _, err := w.file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		return io.ReadAll(io.LimitReader(w.file, imageBillingResponseLimit+1))
	}
	return append(bytes.Clone(w.tail), w.pending...), nil
}
func (w *probePenaltyResponse) validate() error {
	body, err := w.body()
	if err != nil {
		return err
	}
	if w.status >= http.StatusBadRequest {
		return errors.New("probe penalty response is not successful")
	}
	if w.jsonMode {
		if !gjson.ValidBytes(body) || gjson.GetBytes(body, "type").String() != "message" || !gjson.GetBytes(body, "usage").IsObject() {
			return errors.New("probe penalty requires a Claude message with usage")
		}
		return nil
	}
	var usage, stop bool
	for _, block := range bytes.Split(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")), []byte("\n\n")) {
		payload := probeEventData(block)
		switch gjson.GetBytes(payload, "type").String() {
		case "message_delta":
			usage = gjson.GetBytes(payload, "usage.output_tokens").Exists()
		case "message_stop":
			stop = true
		case "error":
			return errors.New("probe penalty response contains an error")
		}
	}
	if !usage || !stop {
		return errors.New("probe penalty requires completed Claude stream usage")
	}
	return nil
}

func rewriteProbeUsage(body []byte, result *service.ForwardResult) ([]byte, error) {
	if result == nil || result.ProbePenalty == nil {
		return body, nil
	}
	original := gjson.GetBytes(body, "usage")
	if !original.IsObject() {
		return nil, errors.New("missing terminal usage")
	}
	body, err := sjson.SetRawBytes(body, "upstream_usage", []byte(original.Raw))
	if err != nil {
		return nil, err
	}
	usage := result.Usage
	usage.OutputTokens = result.ProbePenalty.BillingOutputTokens
	// Keep provider-specific usage fields; overwrite only the billing counters.
	for key, value := range map[string]int{"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens, "cache_creation_input_tokens": usage.CacheCreationInputTokens, "cache_read_input_tokens": usage.CacheReadInputTokens} {
		body, err = sjson.SetBytes(body, "usage."+key, value)
		if err != nil {
			return nil, err
		}
	}
	metadata := map[string]any{"usage_kind": "billing_equivalent", "probe_penalty": result.ProbePenalty}
	return sjson.SetBytes(body, "billing", metadata)
}

func (w *probePenaltyResponse) finish(result *service.ForwardResult) error {
	body, err := w.body()
	if err != nil {
		return err
	}
	if result != nil && result.ProbePenalty != nil {
		if w.jsonMode {
			body, err = rewriteProbeUsage(body, result)
		} else {
			blocks := bytes.Split(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")), []byte("\n\n"))
			for i, block := range blocks {
				payload := probeEventData(block)
				if gjson.GetBytes(payload, "type").String() != "message_delta" {
					continue
				}
				var rewritten []byte
				rewritten, err = rewriteProbeUsage(payload, result)
				if err != nil {
					break
				}
				blocks[i] = append([]byte("event: message_delta\ndata: "), rewritten...)
			}
			body = bytes.Join(blocks, []byte("\n\n"))
		}
	}
	if err != nil {
		return err
	}
	if !w.live {
		w.commitHeaders()
		w.live = true
	}
	_, err = w.ResponseWriter.Write(body)
	w.ResponseWriter.Flush()
	return err
}

func (w *probePenaltyResponse) settlementError() {
	// No terminal success after an unknown/failed settlement. Do not retry a
	// model call: the durable journal may already own the monetary event.
	payload, _ := json.Marshal(gin.H{"type": "error", "error": gin.H{"type": "api_error", "message": "Probe penalty settlement unavailable; do not automatically retry"}})
	if w.live {
		_, _ = w.ResponseWriter.Write(append(append([]byte("event: error\ndata: "), payload...), '\n', '\n'))
	} else {
		w.ResponseWriter.Header().Set("Content-Type", "application/json")
		w.ResponseWriter.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.ResponseWriter.Write(payload)
	}
	w.ResponseWriter.Flush()
}
