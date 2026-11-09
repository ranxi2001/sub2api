package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// GeminiGenerateContentToChatCompletions 把 Gemini generateContent 请求体转成
// Chat Completions 请求体。model 来自 URL，Gemini 请求体本身不带 model。
// 转换结果交给现有 Chat Completions 入口后，再由上游协议分流决定真正的上游端点。
func GeminiGenerateContentToChatCompletions(model string, body []byte, stream bool) ([]byte, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, fmt.Errorf("model is required")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("request body is empty")
	}
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("failed to parse request body")
	}
	if cached := strings.TrimSpace(firstJSONString(body, "cachedContent", "cached_content")); cached != "" {
		return nil, fmt.Errorf("explicit cached content is not supported for this upstream")
	}

	contents := gjson.GetBytes(body, "contents")
	if !contents.Exists() || !contents.IsArray() {
		return nil, fmt.Errorf("contents is required")
	}

	messages := make([]any, 0, contents.Get("#").Int()+1)
	if system := geminiInstructionText(body, "systemInstruction", "system_instruction"); system != "" {
		messages = append(messages, map[string]any{"role": "system", "content": system})
	}

	var pending []geminiPendingCall
	contents.ForEach(func(_, content gjson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(content.Get("role").String()))
		if role == "" {
			role = "user"
		}
		messages = append(messages, geminiContentToChatMessages(role, content.Get("parts"), &pending)...)
		return true
	})
	onlySystem := false
	if len(messages) == 1 {
		msg, ok := messages[0].(map[string]any)
		onlySystem = ok && msg["role"] == "system"
	}
	if len(messages) == 0 || onlySystem {
		return nil, fmt.Errorf("contents is required")
	}

	out := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   stream,
	}
	if stream {
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	if tools := geminiToolsToChatTools(body); len(tools) > 0 {
		out["tools"] = tools
		if choice := geminiToolChoice(body, tools); len(choice) > 0 {
			var decoded any
			if err := json.Unmarshal(choice, &decoded); err == nil {
				out["tool_choice"] = decoded
			}
		}
	}
	applyGeminiGenerationConfig(body, out)

	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal chat completions request: %w", err)
	}
	return encoded, nil
}

// ChatCompletionsJSONToGemini 把 Chat Completions 非流式响应转成 Gemini generateContent 响应。
func ChatCompletionsJSONToGemini(body []byte, fallbackModel string) ([]byte, error) {
	var resp ChatCompletionsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse chat completions response: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("chat completions response has no choices")
	}
	model := strings.TrimSpace(resp.Model)
	if model == "" {
		model = strings.TrimSpace(fallbackModel)
	}
	payload := map[string]any{
		"candidates": geminiCandidatesFromChoices(resp.Choices),
	}
	if usage := geminiUsageFromChat(resp.Usage); usage != nil {
		payload["usageMetadata"] = usage
	}
	if model != "" {
		payload["modelVersion"] = model
	}
	return json.Marshal(payload)
}

// GeminiSSETranslator 把 Chat Completions SSE 增量转成 Gemini SSE（data: {...}\n\n）。
type GeminiSSETranslator struct {
	model string
	buf   bytes.Buffer
	tools map[int]*geminiToolAccum
}

func NewGeminiSSETranslator(model string) *GeminiSSETranslator {
	return &GeminiSSETranslator{model: strings.TrimSpace(model), tools: map[int]*geminiToolAccum{}}
}

// Push 吃进任意切分的上游字节，返回已经可以写给 Gemini 客户端的 SSE。
func (t *GeminiSSETranslator) Push(p []byte) []byte {
	if t == nil || len(p) == 0 {
		return nil
	}
	_, _ = t.buf.Write(p)
	var out bytes.Buffer
	for {
		data := t.buf.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(string(data[:idx]), "\r")
		t.buf.Next(idx + 1)
		if converted := t.translateLine(line); len(converted) > 0 {
			_, _ = out.Write(converted)
		}
	}
	return out.Bytes()
}

// Flush 处理末尾没有换行的残留行。
func (t *GeminiSSETranslator) Flush() []byte {
	if t == nil || t.buf.Len() == 0 {
		return nil
	}
	line := strings.TrimRight(t.buf.String(), "\r\n")
	t.buf.Reset()
	return t.translateLine(line)
}

// EstimateGeminiTotalTokens 按文本长度估算 countTokens。桥接分组没有 Gemini
// countTokens 上游，避免为此再打一次生成请求。
func EstimateGeminiTotalTokens(body []byte) (tokens int, characters int) {
	if !gjson.ValidBytes(body) {
		return 0, 0
	}
	var b strings.Builder
	appendText := func(text string) {
		if text == "" {
			return
		}
		if b.Len() > 0 {
			_ = b.WriteByte('\n')
		}
		_, _ = b.WriteString(text)
	}
	appendText(geminiInstructionText(body, "systemInstruction", "system_instruction"))
	gjson.GetBytes(body, "contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			appendText(part.Get("text").String())
			if call := geminiObject(part, "functionCall", "function_call"); call.Exists() {
				appendText(call.Get("name").String())
				appendText(call.Get("args").Raw)
			}
			if resp := geminiObject(part, "functionResponse", "function_response"); resp.Exists() {
				appendText(resp.Get("name").String())
				appendText(resp.Get("response").Raw)
			}
			return true
		})
		return true
	})
	characters = utf8.RuneCountInString(b.String())
	if characters == 0 {
		return 0, 0
	}
	tokens = (characters + 3) / 4
	return tokens, characters
}

type geminiPendingCall struct {
	name string
	id   string
	used bool
}

type geminiToolAccum struct {
	name string
	args string
}

func geminiContentToChatMessages(role string, parts gjson.Result, pending *[]geminiPendingCall) []any {
	var textParts []string
	var media []map[string]any
	var toolCalls []any
	var toolMessages []any
	parts.ForEach(func(_, part gjson.Result) bool {
		if part.Get("thought").Bool() {
			return true
		}
		if text := part.Get("text").String(); text != "" {
			textParts = append(textParts, text)
		}
		if mediaPart, ok := geminiInlineDataToChatPart(part); ok {
			media = append(media, mediaPart)
		}
		if call := geminiObject(part, "functionCall", "function_call"); call.Exists() {
			name := strings.TrimSpace(call.Get("name").String())
			id := fmt.Sprintf("call_%s_%d", sanitizeToolID(name), len(*pending))
			*pending = append(*pending, geminiPendingCall{name: name, id: id})
			args := "{}"
			if raw := strings.TrimSpace(call.Get("args").Raw); raw != "" && raw != "null" {
				args = raw
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": args,
				},
			})
		}
		if resp := geminiObject(part, "functionResponse", "function_response"); resp.Exists() {
			name := strings.TrimSpace(resp.Get("name").String())
			id := takeGeminiCallID(pending, name)
			content := geminiFunctionResponseContent(resp.Get("response"))
			toolMessages = append(toolMessages, map[string]any{
				"role":         "tool",
				"tool_call_id": id,
				"content":      content,
			})
		}
		return true
	})

	var out []any
	chatRole := "user"
	if role == "model" || role == "assistant" {
		chatRole = "assistant"
	}
	if chatRole == "assistant" || len(toolMessages) == 0 {
		if msg := geminiTextMessage(chatRole, textParts, media, toolCalls); msg != nil {
			out = append(out, msg)
		}
		out = append(out, toolMessages...)
		return out
	}
	// 用户回合里的 functionResponse 在 Chat Completions 里必须是独立的 tool 消息。
	out = append(out, toolMessages...)
	if msg := geminiTextMessage("user", textParts, media, nil); msg != nil {
		out = append(out, msg)
	}
	return out
}

func geminiTextMessage(role string, textParts []string, media []map[string]any, toolCalls []any) map[string]any {
	if len(textParts) == 0 && len(media) == 0 && len(toolCalls) == 0 {
		return nil
	}
	msg := map[string]any{"role": role}
	switch {
	case len(media) > 0:
		parts := make([]any, 0, len(textParts)+len(media))
		if text := strings.Join(textParts, "\n"); text != "" {
			parts = append(parts, map[string]any{"type": "text", "text": text})
		}
		for _, part := range media {
			parts = append(parts, part)
		}
		msg["content"] = parts
	case len(textParts) > 0:
		msg["content"] = strings.Join(textParts, "\n")
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	return msg
}

func geminiInlineDataToChatPart(part gjson.Result) (map[string]any, bool) {
	inline := geminiObject(part, "inlineData", "inline_data")
	if !inline.Exists() {
		file := geminiObject(part, "fileData", "file_data")
		if !file.Exists() {
			return nil, false
		}
		uri := strings.TrimSpace(file.Get("fileUri").String())
		if uri == "" {
			uri = strings.TrimSpace(file.Get("file_uri").String())
		}
		mime := strings.TrimSpace(file.Get("mimeType").String())
		if mime == "" {
			mime = strings.TrimSpace(file.Get("mime_type").String())
		}
		if uri == "" {
			return nil, false
		}
		if strings.HasPrefix(mime, "image/") || mime == "" {
			return map[string]any{"type": "image_url", "image_url": map[string]any{"url": uri}}, true
		}
		return map[string]any{"type": "text", "text": uri}, true
	}
	mime := strings.TrimSpace(inline.Get("mimeType").String())
	if mime == "" {
		mime = strings.TrimSpace(inline.Get("mime_type").String())
	}
	data := strings.TrimSpace(inline.Get("data").String())
	if data == "" {
		return nil, false
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	return map[string]any{
		"type":      "image_url",
		"image_url": map[string]any{"url": "data:" + mime + ";base64," + data},
	}, true
}

func geminiToolsToChatTools(body []byte) []any {
	var tools []any
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		decls := tool.Get("functionDeclarations")
		if !decls.Exists() {
			decls = tool.Get("function_declarations")
		}
		decls.ForEach(func(_, decl gjson.Result) bool {
			name := strings.TrimSpace(decl.Get("name").String())
			if name == "" {
				return true
			}
			fn := map[string]any{"name": name}
			if desc := decl.Get("description").String(); desc != "" {
				fn["description"] = desc
			}
			if params := decl.Get("parameters"); params.Exists() && params.Raw != "" && params.Raw != "null" {
				fn["parameters"] = json.RawMessage(params.Raw)
			}
			tools = append(tools, map[string]any{"type": "function", "function": fn})
			return true
		})
		return true
	})
	return tools
}

func geminiToolChoice(body []byte, tools []any) json.RawMessage {
	cfg := gjson.GetBytes(body, "toolConfig.functionCallingConfig")
	if !cfg.Exists() {
		cfg = gjson.GetBytes(body, "tool_config.function_calling_config")
	}
	if !cfg.Exists() {
		return nil
	}
	mode := strings.ToUpper(strings.TrimSpace(cfg.Get("mode").String()))
	names := cfg.Get("allowedFunctionNames")
	if !names.Exists() {
		names = cfg.Get("allowed_function_names")
	}
	var allowed []string
	names.ForEach(func(_, name gjson.Result) bool {
		if text := strings.TrimSpace(name.String()); text != "" {
			allowed = append(allowed, text)
		}
		return true
	})
	switch mode {
	case "NONE":
		return json.RawMessage(`"none"`)
	case "ANY":
		if len(allowed) == 1 {
			encoded, _ := json.Marshal(map[string]any{
				"type":     "function",
				"function": map[string]any{"name": allowed[0]},
			})
			return encoded
		}
		return json.RawMessage(`"required"`)
	case "AUTO", "":
		if len(allowed) == 1 && len(tools) > 0 {
			encoded, _ := json.Marshal(map[string]any{
				"type":     "function",
				"function": map[string]any{"name": allowed[0]},
			})
			return encoded
		}
		return nil
	default:
		return nil
	}
}

func applyGeminiGenerationConfig(body []byte, out map[string]any) {
	cfg := gjson.GetBytes(body, "generationConfig")
	if !cfg.Exists() {
		cfg = gjson.GetBytes(body, "generation_config")
	}
	if !cfg.Exists() {
		return
	}
	if cfg.Get("temperature").Exists() {
		out["temperature"] = cfg.Get("temperature").Float()
	}
	if cfg.Get("topP").Exists() {
		out["top_p"] = cfg.Get("topP").Float()
	} else if cfg.Get("top_p").Exists() {
		out["top_p"] = cfg.Get("top_p").Float()
	}
	if cfg.Get("maxOutputTokens").Exists() {
		out["max_tokens"] = cfg.Get("maxOutputTokens").Int()
	} else if cfg.Get("max_output_tokens").Exists() {
		out["max_tokens"] = cfg.Get("max_output_tokens").Int()
	}
	if cfg.Get("candidateCount").Exists() && cfg.Get("candidateCount").Int() > 0 {
		out["n"] = cfg.Get("candidateCount").Int()
	} else if cfg.Get("candidate_count").Int() > 0 {
		out["n"] = cfg.Get("candidate_count").Int()
	}
	var stops []string
	stop := cfg.Get("stopSequences")
	if !stop.Exists() {
		stop = cfg.Get("stop_sequences")
	}
	stop.ForEach(func(_, item gjson.Result) bool {
		if text := item.String(); text != "" {
			stops = append(stops, text)
		}
		return true
	})
	if len(stops) == 1 {
		out["stop"] = stops[0]
	} else if len(stops) > 1 {
		out["stop"] = stops
	}
	mime := strings.TrimSpace(cfg.Get("responseMimeType").String())
	if mime == "" {
		mime = strings.TrimSpace(cfg.Get("response_mime_type").String())
	}
	schema := cfg.Get("responseSchema")
	if !schema.Exists() {
		schema = cfg.Get("response_schema")
	}
	if strings.EqualFold(mime, "application/json") && schema.Exists() && schema.Raw != "" && schema.Raw != "null" {
		out["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "response",
				"schema": json.RawMessage(schema.Raw),
			},
		}
	} else if strings.EqualFold(mime, "application/json") {
		out["response_format"] = map[string]any{"type": "json_object"}
	}
}

func geminiCandidatesFromChoices(choices []ChatChoice) []any {
	out := make([]any, 0, len(choices))
	for _, choice := range choices {
		parts := make([]any, 0, 2)
		if text := chatMessageText(choice.Message); text != "" {
			parts = append(parts, map[string]any{"text": text})
		}
		for _, call := range choice.Message.ToolCalls {
			name := strings.TrimSpace(call.Function.Name)
			args := map[string]any{}
			if raw := strings.TrimSpace(call.Function.Arguments); raw != "" {
				if err := json.Unmarshal([]byte(raw), &args); err != nil {
					args = map[string]any{"raw": raw}
				}
			}
			parts = append(parts, map[string]any{
				"functionCall": map[string]any{"name": name, "args": args},
			})
		}
		if len(parts) == 0 {
			parts = append(parts, map[string]any{"text": ""})
		}
		out = append(out, map[string]any{
			"index": choice.Index,
			"content": map[string]any{
				"role":  "model",
				"parts": parts,
			},
			"finishReason": chatFinishReasonToGemini(choice.FinishReason),
		})
	}
	return out
}

func chatMessageText(message ChatMessage) string {
	if len(message.Content) == 0 || string(message.Content) == "null" {
		return ""
	}
	if message.Content[0] == '"' {
		var text string
		if err := json.Unmarshal(message.Content, &text); err == nil {
			return text
		}
	}
	var parts []ChatContentPart
	if err := json.Unmarshal(message.Content, &parts); err == nil {
		texts := make([]string, 0, len(parts))
		for _, part := range parts {
			if part.Type == "" || part.Type == "text" {
				if part.Text != "" {
					texts = append(texts, part.Text)
				}
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

func chatFinishReasonToGemini(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	default:
		return "STOP"
	}
}

func geminiUsageFromChat(usage *ChatUsage) map[string]any {
	if usage == nil {
		return nil
	}
	return map[string]any{
		"promptTokenCount":     usage.PromptTokens,
		"candidatesTokenCount": usage.CompletionTokens,
		"totalTokenCount":      usage.TotalTokens,
	}
}

func (t *GeminiSSETranslator) translateLine(line string) []byte {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
		return nil
	}
	rest, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return nil
	}
	payload := strings.TrimSpace(rest)
	if payload == "" || payload == "[DONE]" {
		return nil
	}
	if gjson.Get(payload, "error").Exists() && !gjson.Get(payload, "choices").Exists() {
		message := gjson.Get(payload, "error.message").String()
		if message == "" {
			message = "Upstream request failed"
		}
		return geminiSSEData(map[string]any{
			"error": map[string]any{"code": 502, "message": message, "status": "INTERNAL"},
		})
	}
	var chunk ChatCompletionsChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return nil
	}
	if chunk.Model != "" {
		t.model = chunk.Model
	}
	var events []any
	for _, choice := range chunk.Choices {
		if text := choice.Delta.Content; text != nil && *text != "" {
			events = append(events, map[string]any{
				"candidates": []any{geminiStreamCandidate(choice.Index, []any{map[string]any{"text": *text}}, "")},
			})
		}
		for _, call := range choice.Delta.ToolCalls {
			idx := 0
			if call.Index != nil {
				idx = *call.Index
			}
			acc := t.tools[idx]
			if acc == nil {
				acc = &geminiToolAccum{}
				t.tools[idx] = acc
			}
			if name := strings.TrimSpace(call.Function.Name); name != "" {
				acc.name = name
			}
			acc.args += call.Function.Arguments
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			parts := make([]any, 0, len(t.tools))
			indexes := make([]int, 0, len(t.tools))
			for idx := range t.tools {
				indexes = append(indexes, idx)
			}
			sort.Ints(indexes)
			for _, idx := range indexes {
				acc := t.tools[idx]
				if acc == nil || acc.name == "" {
					continue
				}
				args := map[string]any{}
				if raw := strings.TrimSpace(acc.args); raw != "" {
					if err := json.Unmarshal([]byte(raw), &args); err != nil {
						args = map[string]any{"raw": raw}
					}
				}
				parts = append(parts, map[string]any{"functionCall": map[string]any{"name": acc.name, "args": args}})
			}
			t.tools = map[int]*geminiToolAccum{}
			if len(parts) == 0 {
				parts = append(parts, map[string]any{"text": ""})
			}
			candidate := geminiStreamCandidate(choice.Index, parts, chatFinishReasonToGemini(*choice.FinishReason))
			event := map[string]any{"candidates": []any{candidate}}
			if usage := geminiUsageFromChat(chunk.Usage); usage != nil {
				event["usageMetadata"] = usage
				if t.model != "" {
					event["modelVersion"] = t.model
				}
			}
			events = append(events, event)
			continue
		}
	}
	if len(chunk.Choices) == 0 {
		if usage := geminiUsageFromChat(chunk.Usage); usage != nil {
			event := map[string]any{"usageMetadata": usage}
			if t.model != "" {
				event["modelVersion"] = t.model
			}
			events = append(events, event)
		}
	}
	if len(events) == 0 {
		return nil
	}
	var out bytes.Buffer
	for _, event := range events {
		_, _ = out.Write(geminiSSEData(event))
	}
	return out.Bytes()
}

func geminiStreamCandidate(index int, parts []any, finish string) map[string]any {
	candidate := map[string]any{
		"index": index,
		"content": map[string]any{
			"role":  "model",
			"parts": parts,
		},
	}
	if finish != "" {
		candidate["finishReason"] = finish
	}
	return candidate
}

func geminiSSEData(payload any) []byte {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	buf := make([]byte, 0, len(encoded)+8)
	buf = append(buf, "data: "...)
	buf = append(buf, encoded...)
	buf = append(buf, '\n', '\n')
	return buf
}

func geminiInstructionText(body []byte, keys ...string) string {
	for _, key := range keys {
		value := gjson.GetBytes(body, key)
		if !value.Exists() {
			continue
		}
		if text := geminiPartsText(value.Get("parts")); text != "" {
			return text
		}
		if value.Type == gjson.String && strings.TrimSpace(value.String()) != "" {
			return strings.TrimSpace(value.String())
		}
	}
	return ""
}

func geminiPartsText(parts gjson.Result) string {
	if !parts.Exists() {
		return ""
	}
	texts := make([]string, 0, parts.Get("#").Int())
	parts.ForEach(func(_, part gjson.Result) bool {
		if part.Get("thought").Bool() {
			return true
		}
		if text := part.Get("text").String(); strings.TrimSpace(text) != "" {
			texts = append(texts, text)
		}
		return true
	})
	return strings.Join(texts, "\n")
}

func geminiObject(part gjson.Result, keys ...string) gjson.Result {
	for _, key := range keys {
		value := part.Get(key)
		if value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func geminiFunctionResponseContent(response gjson.Result) string {
	if !response.Exists() || response.Raw == "" || response.Raw == "null" {
		return ""
	}
	if response.Type == gjson.String {
		return response.String()
	}
	if content := response.Get("content"); content.Type == gjson.String {
		return content.String()
	}
	if output := response.Get("output"); output.Type == gjson.String {
		return output.String()
	}
	return response.Raw
}

func takeGeminiCallID(pending *[]geminiPendingCall, name string) string {
	if pending != nil {
		for i := range *pending {
			if !(*pending)[i].used && (*pending)[i].name == name {
				(*pending)[i].used = true
				return (*pending)[i].id
			}
		}
	}
	return "call_" + sanitizeToolID(name) + "_resp"
}

func sanitizeToolID(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "tool"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_' || r == '-':
			_, _ = b.WriteRune(r)
		default:
			_ = b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "tool"
	}
	return b.String()
}

func firstJSONString(body []byte, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(gjson.GetBytes(body, key).String()); value != "" {
			return value
		}
	}
	return ""
}
