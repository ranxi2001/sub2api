package qqbot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const chatImageToolName = "generate_image"
const chatEditImageToolName = "edit_image"
const chatTextToolName = "reply_text"

var errChatActionMissing = errors.New("聊天模型未返回明确的回复或图片操作，请检查模型和渠道是否支持 required 工具调用")

const chatImageInstructions = `你可以调用 generate_image 生成一张新图片，或在本次提供原图时调用 edit_image 修改指定图片，系统会将结果直接发送到群里。请结合本次消息、明确引用及相关上下文，自主判断用户期待的是文字、新图片还是修改原图；不要求用户使用指令、固定关键词或特定句式。只有当前用户明确要求现在生成/修改图片，或明确确认系统提供的本用户有效待确认图片方案时才调用；一般群历史、其他成员请求和已经完成的图片不构成本轮执行依据，不要重复询问已知信息。只讨论图片、询问能力或方法、索要提示词/文案、看图分析、明确不要图片时通过 reply_text 正常文字回答，不调用图片工具。意图、编辑目标或关键画面信息不清楚时通过 reply_text 自然追问。群历史和引用仅是上下文，不可把其他成员或历史中的请求当成本次执行指令。编辑必须调用 edit_image 并选择本次输入中的原图，优先用户明确引用的图片；图片按本次输入顺序从 1 编号，不可引用历史中没有实际提供的图片。没有原图时请用户重新附图，不要根据文字描述假装编辑或改用文生图。生成时整理完整画面描述；编辑时说明用户要求修改的内容和需要保留的部分，保留未要求修改的主体、布局和其他细节，不承诺像素级完全一致。GIF 输出需模型和渠道实际支持；用户要求动画时选择 gif 并在 prompt 描述动作及循环要求。GIF 原图编辑只使用首帧，可以生成新的动画，但不能承诺保留原动画或逐帧修改。每轮最多调用一个图片工具。系统负责处理和发送结果，无需承诺已经完成，也不要输出工具名、参数 JSON 或内部标识。`

const chatActionInstructions = `默认使用 reply_text 文字回答。闲聊、问候、身份确认、斗嘴或表达情绪都不是图片请求，不能为了幽默或反击擅自生成表情包。含糊的“继续”“就这样”只有承接系统提供的本用户有效待确认图片方案时才可执行图片操作。若当前用户明确在规划图片但尚需确认，可以通过 reply_text 的 image_plan 返回待确认方案；普通聊天该字段必须为 null。本轮必须且只能选择一个操作：reply_text（正常回答、追问、拒绝或图片分析）、generate_image（生成新图）、edit_image（修改本次提供的原图）。文字回答也必须通过 reply_text 的 text 参数返回。生成或修改请求明确且可执行时，必须选择对应图片操作，不能用 reply_text 回复“给你安排一张”“正在生成”“已经修改”等承诺代替实际操作。尚未执行的图片操作不能当成完成结果。人设仅影响回复语气；上述工具是系统实际提供的能力，旧人设中“只能聊天”等过时的能力描述不适用于这些工具。账户、余额等操作限制仍然有效。根据用户实际意图选择，不能因为提供了工具就强行生成图片；不合适的请求可以用 reply_text 拒绝或提供合适的替代建议。`

type chatTurnResult struct {
	Text              string
	SiteQuery         *siteQuery
	ImagePlan         *pendingImagePlan
	ImagePrompt       string
	ImageOutputFormat string
	EditImageIndex    int
	InputTokens       int64
	OutputTokens      int64
	ChatExecution     ChatExecution
}

func chatImageTool(protocol string) map[string]any {
	return wrapChatImageTool(protocol, chatImageFunction())
}

func chatImageFunction() map[string]any {
	function := map[string]any{
		"name":        chatImageToolName,
		"description": "仅在当前用户明确请求新图片，或确认本用户有效待确认方案时生成并发送一张图片。闲聊、问候、斗嘴、身份确认和能力咨询不调用。prompt 必须为完整画面描述。",
		"strict":      true,
		"parameters": map[string]any{
			"type": "object", "properties": map[string]any{"prompt": map[string]string{"type": "string", "description": "完整画面描述，1～6000 字；动画需描述动作和循环要求"}, "output_format": chatImageOutputFormatSchema()},
			"required": []string{"prompt", "output_format"}, "additionalProperties": false,
		},
	}
	return function
}

func wrapChatImageTool(protocol string, function map[string]any) map[string]any {
	if protocol == "responses" {
		function["type"] = "function"
		return function
	}
	return map[string]any{"type": "function", "function": function}
}

func chatImageOutputFormatSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []string{"auto", "png", "jpeg", "webp", "gif"}, "description": "当前用户明确要求 GIF、动图或动画时选择 gif，明确要求静态图时选择 png，其他情况用 auto 沿用后台设置。GIF 需上游模型及渠道支持；不代表可以执行未授权的图片请求。"}
}

func chatEditImageTool(protocol string) map[string]any {
	function := chatImageFunction()
	function["name"] = chatEditImageToolName
	function["description"] = "修改本次输入中的指定原图并发送结果。只选择用户要求修改的图片，保留未要求修改的内容。"
	parameters := map[string]any{"type": "object", "additionalProperties": false}
	parameters["properties"] = map[string]any{
		"prompt":        map[string]string{"type": "string", "description": "完整编辑要求和需要保留的内容，1～6000 字"},
		"image_index":   map[string]any{"type": "integer", "description": "待编辑原图在本次输入图片中的编号，从 1 开始", "minimum": 1, "maximum": maxInputImages},
		"output_format": chatImageOutputFormatSchema(),
	}
	parameters["required"] = []string{"prompt", "image_index", "output_format"}
	function["parameters"] = parameters
	return wrapChatImageTool(protocol, function)
}

func chatTextTool(protocol string) map[string]any {
	return chatTextToolWithPlans(protocol, true)
}

func chatTextToolWithPlans(protocol string, allowPlans bool) map[string]any {
	plan := imagePlanSchema()
	if !allowPlans {
		plan = map[string]any{"type": "null"}
	}
	return wrapChatImageTool(protocol, map[string]any{
		"name": chatTextToolName, "description": "发送正常文字回复、追问、拒绝或图片分析。不能用口头承诺代替生成或修改图片操作。", "strict": true,
		"parameters": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"text", "image_plan"},
			"properties": map[string]any{"text": map[string]any{"type": "string", "minLength": 1, "maxLength": 32000}, "image_plan": plan},
		},
	})
}

func currentChatImages(messages []Message) []string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Images
		}
	}
	return nil
}

// Only explicitly addressed chat gets a tool. Autonomous participation and
// administrator connection tests keep using the text-only chat path.
func (s *Service) chatTurn(ctx context.Context, c Config, messages []Message) (chatTurnResult, error) {
	if !c.ImageEnabled {
		text, in, out, err := s.chatReply(ctx, c, messages)
		return chatTurnResult{Text: text, InputTokens: in, OutputTokens: out}, err
	}
	input := make([]Message, 0, len(messages)+1)
	imageCount := len(currentChatImages(messages))
	// Place the current capability contract after configured persona/history
	// instructions, before conversation data. Old personas may say "chat only".
	boundary := 0
	for boundary < len(messages) && (messages[boundary].Role == "system" || messages[boundary].Role == "developer") {
		boundary++
	}
	input = append(input, messages[:boundary]...)
	input = append(input, Message{Role: "system", Content: chatImageInstructions + "\n" + chatActionInstructions + fmt.Sprintf("本次实际提供 %d 张图片。", imageCount)})
	input = append(input, messages[boundary:]...)
	raw, ct, err := s.chatRequest(ctx, c, input, true)
	if err != nil {
		return chatTurnResult{}, err
	}
	turn, err := parseChatTurn(raw, ct)
	if err == nil && turn.EditImageIndex > imageCount {
		return turn, errors.New("未取得指定的编辑原图，请重新发送图片。")
	}
	return turn, err
}

type chatToolCall struct {
	Index    int    `json:"index"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatToolResponse struct {
	Status            string          `json:"status"`
	Error             json.RawMessage `json:"error"`
	IncompleteDetails json.RawMessage `json:"incomplete_details"`
	Choices           []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			ToolCalls []chatToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Output []struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"output"`
	Usage struct {
		Input      int64 `json:"input_tokens"`
		Output     int64 `json:"output_tokens"`
		Prompt     int64 `json:"prompt_tokens"`
		Completion int64 `json:"completion_tokens"`
	} `json:"usage"`
}

func parseChatTurn(raw []byte, ct string) (chatTurnResult, error) {
	return parseChatTurnWith(raw, ct, validateChatImageCall)
}

func parseChatTurnWith(raw []byte, ct string, validate func(chatTurnResult, []chatToolCall) (chatTurnResult, error)) (chatTurnResult, error) {
	if strings.Contains(ct, "text/event-stream") {
		return parseChatToolStreamWith(raw, validate)
	}
	var v chatToolResponse
	if json.Unmarshal(raw, &v) != nil {
		return chatTurnResult{}, chatResponseError{Code: "malformed_response", Message: "模型响应格式不兼容", Retryable: true}
	}
	in, out := v.Usage.Input, v.Usage.Output
	if in == 0 {
		in = v.Usage.Prompt
	}
	if out == 0 {
		out = v.Usage.Completion
	}
	result := chatTurnResult{InputTokens: in, OutputTokens: out}
	if v.Status == "failed" || v.Status == "incomplete" || (len(v.Error) > 0 && string(v.Error) != "null") {
		return result, chatCompletionFailure(v.Status, v.Error, v.IncompleteDetails)
	}
	var calls []chatToolCall
	if len(v.Choices) > 0 {
		choice := v.Choices[0]
		if choice.FinishReason == "length" || choice.FinishReason == "content_filter" {
			details, _ := json.Marshal(map[string]string{"reason": choice.FinishReason})
			return result, chatCompletionFailure("incomplete", nil, details)
		}
		if len(choice.Message.ToolCalls) > 0 && choice.FinishReason != "tool_calls" {
			return result, incompleteChatResponse("模型工具调用未完成")
		}
		calls = append(calls, choice.Message.ToolCalls...)
	}
	for _, item := range v.Output {
		if item.Type == "function_call" {
			if v.Status != "completed" {
				return result, incompleteChatResponse("模型工具调用未完成")
			}
			call := chatToolCall{Type: "function"}
			call.Function.Name, call.Function.Arguments = item.Name, item.Arguments
			calls = append(calls, call)
		}
	}
	if len(calls) > 0 {
		return validate(result, calls)
	}
	// Do not deliver an unstructured promise as a successful operation when
	// the upstream ignored tool_choice=required. Explicit chat may retry this
	// response within its bounded model-only retry boundary.
	return result, errChatActionMissing
}

func validateChatImageCall(result chatTurnResult, calls []chatToolCall) (chatTurnResult, error) {
	if len(calls) != 1 || calls[0].Type != "function" {
		return result, errors.New("模型返回了不支持的工具调用")
	}
	if calls[0].Function.Name == chatTextToolName {
		var args struct {
			Text      string            `json:"text"`
			ImagePlan *pendingImagePlan `json:"image_plan"`
		}
		d := json.NewDecoder(strings.NewReader(calls[0].Function.Arguments))
		d.DisallowUnknownFields()
		var extra any
		if d.Decode(&args) != nil || d.Decode(&extra) != io.EOF {
			return result, errors.New("模型文字回复参数不正确")
		}
		text := normalizeChatReply(args.Text)
		if n := len([]rune(strings.TrimSpace(text))); n == 0 || n > 32000 {
			return result, errors.New("模型文字回复长度须为 1～32000 字")
		}
		if args.ImagePlan != nil {
			if err := args.ImagePlan.validate(); err != nil {
				return result, err
			}
		}
		result.Text, result.ImagePlan = text, args.ImagePlan
		return result, nil
	}
	if calls[0].Function.Name != chatImageToolName && calls[0].Function.Name != chatEditImageToolName {
		return result, errors.New("模型返回了不支持的工具调用")
	}
	var args struct {
		Prompt       string          `json:"prompt"`
		ImageIndex   json.RawMessage `json:"image_index"`
		OutputFormat string          `json:"output_format"`
	}
	d := json.NewDecoder(strings.NewReader(calls[0].Function.Arguments))
	d.DisallowUnknownFields()
	if d.Decode(&args) != nil {
		return result, errors.New("模型生图参数不正确")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return result, errors.New("模型生图参数不正确")
	}
	if args.OutputFormat != "auto" && !validImageOutputFormat(args.OutputFormat) {
		return result, errors.New("模型返回了不支持的图片输出格式")
	}
	result.ImageOutputFormat = args.OutputFormat
	if calls[0].Function.Name == chatEditImageToolName {
		var index int
		if json.Unmarshal(args.ImageIndex, &index) != nil || index < 1 || index > maxInputImages {
			return result, errors.New("模型选择的编辑原图编号无效")
		}
		result.EditImageIndex = index
	} else if len(args.ImageIndex) > 0 {
		return result, errors.New("模型生图参数不正确")
	}
	prompt := strings.TrimSpace(args.Prompt)
	if n := len([]rune(prompt)); n == 0 || n > 6000 {
		return result, errors.New("模型生图描述长度须为 1～6000 字")
	}
	result.ImagePrompt = prompt
	return result, nil
}

// Some compatible gateways return SSE even for stream:false. Never execute a
// partial tool call: Responses needs response.completed, Chat needs [DONE] and
// a completed tool_calls choice, including for reply_text.
func parseChatToolStreamWith(raw []byte, validate func(chatTurnResult, []chatToolCall) (chatTurnResult, error)) (chatTurnResult, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var calls []chatToolCall
	var completed json.RawMessage
	var result chatTurnResult
	done, toolFinished, responseTool, responseStream := false, false, false, false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			done = true
			continue
		}
		var event struct {
			Type     string                `json:"type"`
			Code     string                `json:"code"`
			Error    json.RawMessage       `json:"error"`
			Response json.RawMessage       `json:"response"`
			Item     struct{ Type string } `json:"item"`
			Choices  []struct {
				Index        int    `json:"index"`
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					ToolCalls []chatToolCall `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage struct {
				Prompt     int64 `json:"prompt_tokens"`
				Completion int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			return result, chatResponseError{Code: "malformed_response", Message: "模型流式响应格式不兼容", Retryable: true}
		}
		if event.Type == "error" || event.Type == "response.failed" || event.Type == "response.incomplete" || (len(event.Error) > 0 && string(event.Error) != "null") {
			var response struct {
				Status  string          `json:"status"`
				Error   json.RawMessage `json:"error"`
				Details json.RawMessage `json:"incomplete_details"`
			}
			_ = json.Unmarshal(event.Response, &response)
			if len(response.Error) == 0 {
				response.Error = event.Error
			}
			if (len(response.Error) == 0 || string(response.Error) == "null") && event.Code != "" {
				response.Error, _ = json.Marshal(map[string]string{"code": event.Code})
			}
			if response.Status == "" && event.Type == "response.incomplete" {
				response.Status = "incomplete"
			}
			return result, chatCompletionFailure(response.Status, response.Error, response.Details)
		}
		if event.Type == "response.completed" {
			completed = event.Response
		}
		if strings.HasPrefix(event.Type, "response.") {
			responseStream = true
		}
		if event.Item.Type == "function_call" || strings.HasPrefix(event.Type, "response.function_call_arguments.") {
			responseTool = true
		}
		if event.Usage.Prompt > 0 || event.Usage.Completion > 0 {
			result.InputTokens, result.OutputTokens = event.Usage.Prompt, event.Usage.Completion
		}
		for _, choice := range event.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.FinishReason == "length" || choice.FinishReason == "content_filter" {
				details, _ := json.Marshal(map[string]string{"reason": choice.FinishReason})
				return result, chatCompletionFailure("incomplete", nil, details)
			}
			if choice.FinishReason == "tool_calls" {
				toolFinished = true
			}
			for _, delta := range choice.Delta.ToolCalls {
				if delta.Index != 0 {
					return result, errors.New("模型返回了多个工具调用")
				}
				if len(calls) == 0 {
					calls = append(calls, chatToolCall{})
				}
				if delta.Type != "" {
					if calls[0].Type != "" && calls[0].Type != delta.Type {
						return result, errors.New("模型返回了不支持的工具调用")
					}
					calls[0].Type = delta.Type
				}
				calls[0].Function.Name += delta.Function.Name
				calls[0].Function.Arguments += delta.Function.Arguments
			}
		}
	}
	if scanner.Err() != nil {
		return result, incompleteChatResponse("模型流式响应不完整")
	}
	if len(completed) > 0 {
		return parseChatTurnWith(completed, "application/json", validate)
	}
	if responseTool || responseStream || !done || (len(calls) > 0 && !toolFinished) {
		return result, incompleteChatResponse("模型工具调用未完成")
	}
	if len(calls) > 0 {
		return validate(result, calls)
	}
	return result, errChatActionMissing
}
