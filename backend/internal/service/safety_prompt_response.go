package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const safetyPromptMaxRequestBody = 32 << 20
const safetyPromptMaxJSONResponse = 16 << 20
const safetyPromptMaxSSEBuffer = 1 << 20
const safetyPromptMaxResponsePeek = 512

type safetyPromptRequestInfo struct {
	Marker        string
	Model         string
	RequestID     string
	Summary       string
	RequestSHA256 string
	AccountID     int64
	Protocol      safetyPromptProtocol
	RetentionDays int
}

func (s *OpenAIGatewayService) applySafetyPromptToRequest(req *http.Request, account *Account) (*http.Request, *safetyPromptRequestInfo) {
	if s == nil || req == nil || account == nil || s.safetyPromptStore == nil || s.settingService == nil {
		return req, nil
	}
	if !account.IsOpenAIOAuthLike() || req.Method != http.MethodPost || req.URL == nil {
		return req, nil
	}
	if s.pluginManager != nil && s.pluginManager.ShouldRouteOpenAIOAuth(account) {
		// Avoid double injection when the legacy safety-prompt transport plugin is active.
		return req, nil
	}
	protocol := safetyPromptProtocolForPath(req.URL.Path)
	if protocol == safetyPromptProtocolUnknown {
		return req, nil
	}
	cfg := s.settingService.GetSafetyPromptConfig(req.Context())
	if !cfg.Enabled {
		return req, nil
	}
	if req.Header.Get("Content-Encoding") != "" || req.Body == nil || req.Body == http.NoBody {
		return req, nil
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, safetyPromptMaxRequestBody+1))
	if err != nil || len(body) == 0 || len(body) > safetyPromptMaxRequestBody {
		// The body was only partially read; put the original bytes back so the
		// request can still be sent unchanged.
		restoreSafetyPromptBody(req, body, req.Body)
		return req, nil
	}
	_ = req.Body.Close()
	signal := cfg.SignalEnabled && protocol == safetyPromptProtocolResponses
	marker := ""
	if signal {
		marker, err = newSafetyPromptID()
		if err != nil {
			restoreSafetyPromptBody(req, body, nil)
			return req, nil
		}
	}
	next, err := injectSafetyPrompt(body, protocol, signal, marker)
	if err != nil {
		restoreSafetyPromptBody(req, body, nil)
		return req, nil
	}
	request := req.Clone(req.Context())
	request.Body = io.NopCloser(bytes.NewReader(next))
	request.ContentLength = int64(len(next))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(next)), nil
	}
	request.Header.Del("Content-Length")
	request.Header.Set("Content-Length", strconv.Itoa(len(next)))
	info := &safetyPromptRequestInfo{
		Marker:        marker,
		Model:         gjson.GetBytes(body, "model").String(),
		RequestID:     firstNonEmptyString(req.Header.Get("x-request-id"), req.Header.Get("X-Request-Id")),
		Summary:       safetyPromptRequestSummary(body),
		RequestSHA256: HashUsageRequestPayload(body),
		AccountID:     account.ID,
		Protocol:      protocol,
		RetentionDays: cfg.RetentionDays,
	}
	return request, info
}

type safetyPromptRestoredBody struct {
	reader io.Reader
	closer io.Closer
}

func (b *safetyPromptRestoredBody) Read(data []byte) (int, error) { return b.reader.Read(data) }
func (b *safetyPromptRestoredBody) Close() error                  { return b.closer.Close() }

// restoreSafetyPromptBody rebuilds a request body that was (partially) consumed
// while checking whether the safety prompt could be injected. When remainder is
// non-nil its unread bytes are replayed after prefix.
func restoreSafetyPromptBody(req *http.Request, prefix []byte, remainder io.ReadCloser) {
	if req == nil {
		return
	}
	if remainder == nil {
		req.Body = io.NopCloser(bytes.NewReader(prefix))
		req.ContentLength = int64(len(prefix))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(prefix)), nil
		}
		return
	}
	req.Body = &safetyPromptRestoredBody{reader: io.MultiReader(bytes.NewReader(prefix), remainder), closer: remainder}
}

func (s *OpenAIGatewayService) wrapSafetyPromptResponse(resp *http.Response, info *safetyPromptRequestInfo) *http.Response {
	if s == nil || resp == nil || info == nil || info.Marker == "" || resp.Body == nil {
		return resp
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || resp.Header.Get("Content-Encoding") != "" {
		return resp
	}
	resp.ContentLength = -1
	resp.Header.Del("Content-Length")
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		resp.Body = newSafetyPromptSSEBody(resp.Body, s.safetyPromptStore, info)
		return resp
	}
	// Some upstreams omit or mislabel the SSE Content-Type. Detect the body
	// incrementally instead of sending every such response through ReadAll.
	resp.Body = newSafetyPromptResponseBody(resp.Body, s.safetyPromptStore, info, true)
	return resp
}

func nowUTC() time.Time { return time.Now().UTC() }

type safetyPromptJSONBody struct {
	body        io.ReadCloser
	store       *SafetyPromptStore
	info        *safetyPromptRequestInfo
	once        sync.Once
	out         *bytes.Reader
	passthrough io.Reader
	err         error
}

func newSafetyPromptJSONBody(body io.ReadCloser, store *SafetyPromptStore, info *safetyPromptRequestInfo) *safetyPromptJSONBody {
	return &safetyPromptJSONBody{body: body, store: store, info: info}
}

func (b *safetyPromptJSONBody) Read(data []byte) (int, error) {
	b.once.Do(b.prepare)
	if b.err != nil {
		return 0, b.err
	}
	if b.passthrough != nil {
		return b.passthrough.Read(data)
	}
	return b.out.Read(data)
}

func (b *safetyPromptJSONBody) Close() error {
	if b.body != nil {
		return b.body.Close()
	}
	return nil
}

func (b *safetyPromptJSONBody) prepare() {
	raw, err := io.ReadAll(io.LimitReader(b.body, safetyPromptMaxJSONResponse+1))
	if err != nil || len(raw) > safetyPromptMaxJSONResponse {
		// Too large or interrupted: replay what was read and stream the rest
		// unchanged instead of truncating the upstream response.
		b.passthrough = io.MultiReader(bytes.NewReader(raw), b.body)
		return
	}
	next, matched := rewriteSafetyPromptJSON(raw, b.info)
	if matched {
		recordSafetyPromptEvent(b.store, b.info)
		b.out = bytes.NewReader(next)
		return
	}
	b.out = bytes.NewReader(raw)
}

type safetyPromptResponseBody struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	once   sync.Once
	source io.ReadCloser
	info   *safetyPromptRequestInfo
	store  *SafetyPromptStore
}

func newSafetyPromptSSEBody(source io.ReadCloser, store *SafetyPromptStore, info *safetyPromptRequestInfo) *safetyPromptResponseBody {
	return newSafetyPromptResponseBody(source, store, info, false)
}

func newSafetyPromptResponseBody(source io.ReadCloser, store *SafetyPromptStore, info *safetyPromptRequestInfo, detectFormat bool) *safetyPromptResponseBody {
	pr, pw := io.Pipe()
	body := &safetyPromptResponseBody{reader: pr, writer: pw, source: source, info: info, store: store}
	go body.run(detectFormat)
	return body
}

func (b *safetyPromptResponseBody) Read(data []byte) (int, error) { return b.reader.Read(data) }

func (b *safetyPromptResponseBody) Close() error {
	b.once.Do(func() {
		_ = b.source.Close()
		_ = b.writer.Close()
	})
	return nil
}

// safetyPromptLooksLikeSSE only peeks: all bytes, including a BOM or leading
// blank lines, remain available to the selected response processor. Stop as
// soon as a field is recognized, without waiting for a fixed-size sample or EOF.
func safetyPromptLooksLikeSSE(reader *bufio.Reader) bool {
	for size := 1; size <= safetyPromptMaxResponsePeek; size++ {
		prefix, err := reader.Peek(size)
		if err != nil {
			return false
		}
		if prefix[0] == 0xef {
			if len(prefix) < 3 {
				continue
			}
			if !bytes.HasPrefix(prefix, []byte("\xef\xbb\xbf")) {
				return false
			}
			prefix = prefix[3:]
		}
		prefix = bytes.TrimLeft(prefix, " \t\r\n")
		if len(prefix) == 0 {
			continue
		}
		possible := false
		for _, field := range []string{":", "data:", "event:", "id:", "retry:"} {
			if bytes.HasPrefix(prefix, []byte(field)) {
				return true
			}
			possible = possible || bytes.HasPrefix([]byte(field), prefix)
		}
		if !possible {
			return false
		}
	}
	return false
}

func (b *safetyPromptResponseBody) run(detectFormat bool) {
	defer func() { _ = b.writer.Close() }()
	reader := bufio.NewReader(b.source)
	if detectFormat && !safetyPromptLooksLikeSSE(reader) {
		body := newSafetyPromptJSONBody(&safetyPromptRestoredBody{reader: reader, closer: b.source}, b.store, b.info)
		if _, err := io.Copy(b.writer, body); err != nil {
			_ = b.writer.CloseWithError(err)
		}
		return
	}
	var frame bytes.Buffer
	var buffered [][]byte
	var text strings.Builder
	bufferedBytes := 0
	buffering, decided := false, false
	flushBuffered := func() {
		for _, item := range buffered {
			_, _ = b.writer.Write(item)
		}
		buffered = nil
		bufferedBytes = 0
		buffering = false
		decided = true
	}
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			_, _ = frame.Write(line)
			if isSSEFrameEnd(line) {
				raw := append([]byte(nil), frame.Bytes()...)
				frame.Reset()
				if decided {
					_, _ = b.writer.Write(raw)
				} else if !buffering {
					if !safetyPromptSSEFrameHasAssistantText(raw) {
						_, _ = b.writer.Write(raw)
					} else {
						buffering = true
						buffered = append(buffered, raw)
						bufferedBytes += len(raw)
						_, _ = text.WriteString(safetyPromptSSEFrameDelta(raw))
						if bufferedBytes > safetyPromptMaxSSEBuffer || !strings.HasPrefix(b.info.Marker, text.String()) {
							flushBuffered()
						}
					}
				} else {
					buffered = append(buffered, raw)
					bufferedBytes += len(raw)
					_, _ = text.WriteString(safetyPromptSSEFrameDelta(raw))
					prefixOK := strings.HasPrefix(b.info.Marker, text.String())
					completed := safetyPromptSSEFrameCompleted(raw)
					if bufferedBytes > safetyPromptMaxSSEBuffer || !prefixOK {
						flushBuffered()
					} else if completed {
						completedText, completeOK := safetyPromptResponsesMessageText(gjson.GetBytes(safetyPromptSSEData(raw), "response.output"))
						if text.String() == b.info.Marker && completeOK && completedText == b.info.Marker {
							rewritten, ok := rewriteSafetyPromptFrames(buffered, b.info.Marker)
							if ok {
								for _, item := range rewritten {
									_, _ = b.writer.Write(item)
								}
								recordSafetyPromptEvent(b.store, b.info)
								buffered = nil
								buffering = false
								decided = true
							} else {
								flushBuffered()
							}
						} else {
							flushBuffered()
						}
					}
				}
			}
		}
		if err != nil {
			if frame.Len() > 0 {
				if buffering {
					buffered = append(buffered, append([]byte(nil), frame.Bytes()...))
				} else {
					_, _ = b.writer.Write(frame.Bytes())
				}
			}
			if buffering && !decided {
				flushBuffered()
			}
			if err != io.EOF {
				_ = b.writer.CloseWithError(err)
			}
			return
		}
	}
}

func isSSEFrameEnd(line []byte) bool {
	trimmed := strings.TrimRight(string(line), "\r\n")
	return trimmed == ""
}

func safetyPromptSSEFrameHasAssistantText(frame []byte) bool {
	payload := safetyPromptSSEData(frame)
	if len(payload) == 0 {
		return false
	}
	return gjson.GetBytes(payload, "type").String() == "response.output_text.delta"
}

func safetyPromptSSEFrameDelta(frame []byte) string {
	payload := safetyPromptSSEData(frame)
	if len(payload) == 0 {
		return ""
	}
	return gjson.GetBytes(payload, "delta").String()
}

func safetyPromptSSEFrameCompleted(frame []byte) bool {
	payload := safetyPromptSSEData(frame)
	if len(payload) == 0 {
		return false
	}
	return gjson.GetBytes(payload, "type").String() == "response.completed"
}

func safetyPromptSSEData(frame []byte) []byte {
	var data []byte
	for _, line := range strings.Split(string(frame), "\n") {
		if strings.HasPrefix(line, "data:") {
			part := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if part != "" {
				if len(data) > 0 {
					data = append(data, '\n')
				}
				data = append(data, part...)
			}
		}
	}
	return data
}

func rewriteSafetyPromptFrames(frames [][]byte, marker string) ([][]byte, bool) {
	refusal := safetyPromptRefusal + marker
	out := make([][]byte, 0, len(frames))
	replaced := false
	for _, frame := range frames {
		payload := safetyPromptSSEData(frame)
		if len(payload) == 0 {
			out = append(out, frame)
			continue
		}
		var root map[string]json.RawMessage
		if json.Unmarshal(payload, &root) != nil {
			return frames, false
		}
		eventType := gjson.GetBytes(payload, "type").String()
		switch eventType {
		case "response.output_text.delta":
			if !replaced {
				root["delta"] = safetyPromptJSON(refusal)
				replaced = true
			} else {
				root["delta"] = safetyPromptJSON("")
			}
		case "response.output_text.done":
			root["text"] = safetyPromptJSON(refusal)
		case "response.content_part.done":
			updated, err := sjson.SetBytes(payload, "part.text", refusal)
			if err != nil {
				return frames, false
			}
			root = nil
			payload = updated
		case "response.output_item.done":
			item := map[string]any{
				"type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": refusal, "annotations": []any{}}},
			}
			if id := gjson.GetBytes(payload, "item.id").String(); id != "" {
				item["id"] = id
			}
			root["item"] = safetyPromptJSON(item)
		case "response.completed":
			item := map[string]any{
				"type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": refusal, "annotations": []any{}}},
			}
			if id := gjson.GetBytes(payload, "response.output.0.id").String(); id != "" {
				item["id"] = id
			}
			updated, err := sjson.SetBytes(payload, "response.output", []any{item})
			if err != nil {
				return frames, false
			}
			root = nil
			payload = updated
		}
		if root != nil {
			next, err := json.Marshal(root)
			if err != nil {
				return frames, false
			}
			payload = next
		}
		out = append(out, rebuildSSEFrame(frame, payload))
	}
	return out, replaced
}

func rebuildSSEFrame(original, payload []byte) []byte {
	lines := strings.Split(string(original), "\n")
	var out strings.Builder
	inserted := false
	for _, line := range lines {
		if strings.HasPrefix(line, "data:") {
			if !inserted {
				_, _ = out.WriteString("data: ")
				_, _ = out.Write(payload)
				_ = out.WriteByte('\n')
				inserted = true
			}
			continue
		}
		_, _ = out.WriteString(line)
		_ = out.WriteByte('\n')
	}
	if !inserted {
		return original
	}
	return []byte(out.String())
}

func rewriteSafetyPromptJSON(raw []byte, info *safetyPromptRequestInfo) ([]byte, bool) {
	if info == nil || info.Marker == "" {
		return raw, false
	}
	text, ok := safetyPromptAssistantText(raw, info.Protocol)
	if !ok || text != info.Marker {
		return raw, false
	}
	refusal := safetyPromptRefusal + info.Marker
	next, ok := replaceSafetyPromptAssistantText(raw, info.Protocol, refusal)
	if !ok {
		return raw, false
	}
	return next, true
}

func safetyPromptAssistantText(raw []byte, protocol safetyPromptProtocol) (string, bool) {
	switch protocol {
	case safetyPromptProtocolResponses:
		return safetyPromptResponsesMessageText(gjson.GetBytes(raw, "output"))
	case safetyPromptProtocolChat:
		message := gjson.GetBytes(raw, "choices.0.message")
		if len(message.Get("tool_calls").Array()) > 0 || message.Get("function_call").IsObject() {
			return "", false
		}
		content := message.Get("content")
		return content.String(), content.Exists()
	case safetyPromptProtocolAnthropic:
		content := gjson.GetBytes(raw, "content")
		var texts []string
		valid := true
		content.ForEach(func(_, item gjson.Result) bool {
			switch item.Get("type").String() {
			case "text":
				texts = append(texts, item.Get("text").String())
			case "thinking", "redacted_thinking":
			default:
				valid = false
				return false
			}
			return true
		})
		if !valid {
			return "", false
		}
		return strings.Join(texts, ""), len(texts) > 0
	default:
		return "", false
	}
}

// safetyPromptResponsesMessageText implements the exact-match boundary: one
// assistant message with text, optional reasoning items, and no tool outputs.
func safetyPromptResponsesMessageText(output gjson.Result) (string, bool) {
	if !output.IsArray() {
		return "", false
	}
	var texts []string
	messages := 0
	valid := true
	output.ForEach(func(_, item gjson.Result) bool {
		switch item.Get("type").String() {
		case "message":
			if item.Get("role").String() != "assistant" {
				valid = false
				return false
			}
			messages++
			item.Get("content").ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "output_text" {
					texts = append(texts, part.Get("text").String())
				}
				return true
			})
		case "reasoning":
		default:
			valid = false
			return false
		}
		return true
	})
	if !valid || messages != 1 || len(texts) == 0 {
		return "", false
	}
	return strings.Join(texts, ""), true
}

func replaceSafetyPromptAssistantText(raw []byte, protocol safetyPromptProtocol, refusal string) ([]byte, bool) {
	switch protocol {
	case safetyPromptProtocolResponses:
		output := []any{map[string]any{
			"type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": refusal, "annotations": []any{}}},
		}}
		next, err := sjson.SetBytes(raw, "output", output)
		return next, err == nil
	case safetyPromptProtocolChat:
		next, err := sjson.SetBytes(raw, "choices.0.message.content", refusal)
		return next, err == nil
	case safetyPromptProtocolAnthropic:
		next, err := sjson.SetBytes(raw, "content", []any{map[string]any{"type": "text", "text": refusal}})
		return next, err == nil
	default:
		return raw, false
	}
}

func recordSafetyPromptEvent(store *SafetyPromptStore, info *safetyPromptRequestInfo) {
	if store == nil || info == nil || info.Marker == "" {
		return
	}
	store.Record(SafetyPromptEvent{
		Time:              nowUTC(),
		ID:                info.Marker,
		RequestID:         info.RequestID,
		Model:             info.Model,
		UpstreamAccountID: info.AccountID,
		Identity:          "unknown",
		Action:            safetyPromptEventAction,
		Summary:           info.Summary,
		RequestSHA256:     info.RequestSHA256,
	}, info.RetentionDays)
}
