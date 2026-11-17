package service

import (
	"encoding/json"
	"errors"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// This local protocol refusal must never count against the upstream account.
var ErrOpenAIWSUnsupportedWarmup = errors.New("unsupported websocket HTTP bridge warmup")

type openAIWSHTTPWarmup struct {
	responseID string
	defaults   []byte
}

func openAIWSWarmupRefusal() error {
	return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation,
		"HTTP bridge supports only an initial empty generate=false warmup; no generation was sent", ErrOpenAIWSUnsupportedWarmup)
}

// A non-generating empty prefill has no provider response/owner to publish.
// Acknowledge it locally and retain only its bounded, same-socket defaults.
func (w *openAIWSHTTPWarmup) accept(source []byte, model string, turn int, write func([]byte) error) (*OpenAIForwardResult, bool, error) {
	if gjson.GetBytes(source, "generate").Type != gjson.False {
		return nil, false, nil
	}
	input := gjson.GetBytes(source, "input")
	if turn != 1 || len(source) > openAIWSLocalHistoryLimit || !openAIEncryptedJSONUnambiguous(source) ||
		!input.IsArray() || len(input.Array()) != 0 || gjson.GetBytes(source, "previous_response_id").Exists() ||
		gjson.GetBytes(source, "conversation").Exists() || gjson.GetBytes(source, "prompt").Exists() {
		return nil, true, openAIWSWarmupRefusal()
	}
	w.responseID = "resp_sub2api_prewarm_" + uuid.NewString()
	w.defaults = source // The ingress owns an immutable source copy.
	response := map[string]any{"id": w.responseID, "object": "response", "created_at": time.Now().Unix(), "model": model, "status": "in_progress", "output": []any{}, "error": nil}
	for n, event := range []string{"response.created", "response.completed"} {
		if n == 1 {
			response["status"] = "completed"
			response["usage"] = map[string]int{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
		}
		raw, err := json.Marshal(map[string]any{"type": event, "sequence_number": n, "response": response})
		if err != nil {
			return nil, true, err
		}
		if err = write(raw); err != nil {
			return nil, true, NewOpenAIWSClientCloseError(coderws.StatusNormalClosure, "client disconnected during local warmup", err)
		}
	}
	return &OpenAIForwardResult{RequestID: w.responseID, Model: model, Stream: true, OpenAIWSMode: true, wsLocalWarmup: true}, true, nil
}

// Resolve only the private warmup anchor returned on this socket. Unknown
// pointers retain their existing owner checks; nothing is learned in Redis.
func (w *openAIWSHTTPWarmup) nextFrame(raw []byte, limit int64) ([]byte, error) {
	if w.defaults == nil {
		return raw, nil
	}
	if !openAIEncryptedJSONUnambiguous(raw) {
		return nil, openAIWSWarmupRefusal()
	}
	var previous, current map[string]json.RawMessage
	if json.Unmarshal(w.defaults, &previous) != nil || json.Unmarshal(raw, &current) != nil || current == nil {
		return nil, openAIWSWarmupRefusal()
	}
	for key, value := range previous {
		switch key {
		case "type", "generate", "input", "previous_response_id", "conversation", "prompt":
			continue
		}
		if _, present := current[key]; !present {
			current[key] = value
		}
	}
	if gjson.GetBytes(raw, "previous_response_id").String() == w.responseID {
		delete(current, "previous_response_id")
	}
	next, err := json.Marshal(current)
	if err != nil {
		return nil, err
	}
	if limit > 0 && int64(len(next)) > limit {
		return nil, NewOpenAIWSClientCloseError(coderws.StatusMessageTooBig, "expanded websocket prefill exceeds the message limit", ErrOpenAIWSUnsupportedWarmup)
	}
	w.defaults = nil
	w.responseID = ""
	return next, nil
}
