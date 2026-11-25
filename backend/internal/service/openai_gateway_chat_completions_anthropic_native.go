package service

// 国产供应商 Anthropic 协议账号的 CC 入站反向路径。
//
// 客户端说 OpenAI Chat Completions、上游是供应商原生 Anthropic 端点
// （api_protocol=anthropic）时的交叉组合：请求 CC→Responses→Anthropic 转换，
// 响应 Anthropic→Responses→CC 转换。转换链与 Anthropic 平台的
// gateway_forward_as_chat_completions.go 完全一致（复用同一组 apicompat
// 状态机），仅上游发送/错误处理对齐 OpenAI 网关语义。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// forwardChatCompletionsViaNativeAnthropic serves OpenAI /v1/chat/completions
// clients through a CN provider's native Anthropic endpoint.
//
// Conversion chain:
//
//	Request:  Chat Completions → Responses → Anthropic (chained)
//	Response: Anthropic events → Responses events → CC chunks (chained state machines)
func (s *OpenAIGatewayService) forwardChatCompletionsViaNativeAnthropic(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()
	SetActualOpenAIUpstreamEndpoint(c, "/v1/messages")

	// 1. Parse Chat Completions request
	var ccReq apicompat.ChatCompletionsRequest
	if err := parseChatRequestForBridge(body, account.Platform, &ccReq); err != nil {
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, fmt.Errorf("parse chat completions request: %w", err)
	}
	originalModel := ccReq.Model
	if strings.TrimSpace(originalModel) == "" {
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	clientStream := ccReq.Stream
	if err := apicompat.ValidateChatCompletionsAnthropicMedia(&ccReq); err != nil {
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}

	// 2. Convert CC → Responses → Anthropic (chained conversion)
	responsesReq, err := chatCompletionsToResponses(s.cfg, &ccReq)
	if err != nil {
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "Failed to convert request")
		return nil, fmt.Errorf("convert chat completions to responses: %w", err)
	}
	if err := apicompat.PreserveChatCompletionsAnthropicToolMedia(&ccReq, responsesReq); err != nil {
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "Failed to convert tool media")
		return nil, err
	}
	// Resolve the mapped model before choosing its thinking/tool protocol.
	billingModel := resolveOpenAIForwardModel(account, originalModel, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	if err := validateClaude55Request(body, upstreamModel); err != nil {
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	responsesReq.Model = upstreamModel
	anthropicReq, err := apicompat.ResponsesToAnthropicRequestWithOptions(responsesReq, apicompat.ResponsesToAnthropicOptions{StrictMedia: true})
	if err != nil {
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("convert responses to anthropic: %w", err)
	}

	// 4. Force upstream streaming（客户端原始终决定响应格式；
	// 上游恒为流式，非流式由缓冲路径组装）。
	anthropicReq.Stream = true
	reqStream := true

	logger.L().Debug("openai chat_completions: forwarding via native anthropic endpoint",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("client_stream", clientStream),
	)

	anthropicBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}
	anthropicBody = withChatCompletionsMetadataUserID(body, anthropicBody)

	// 与 /v1/messages 直通路径相同的 pre-filter。
	anthropicBody = StripEmptyTextBlocks(anthropicBody)
	anthropicBody = FilterWebSearchHistoryBlocks(anthropicBody, upstreamModel)
	anthropicBody = enforceCacheControlLimit(anthropicBody)

	apiKey := strings.TrimSpace(account.GetOpenAIProtocolAPIKey())
	if apiKey == "" {
		return nil, fmt.Errorf("account %d missing api_key", account.ID)
	}
	targetURL, err := s.nativeAnthropicTargetURL(account)
	if err != nil {
		return nil, err
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	upstreamCtx, releaseUpstreamCtx := detachStreamUpstreamContext(ctx, reqStream)
	upstreamReq, forwardedBody, err := s.buildNativeAnthropicUpstreamRequest(upstreamCtx, c, account, anthropicBody, apiKey, targetURL, body)
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}

	if err := s.checkAPIKeyLongContextBeforeSend(ctx, c, forwardedBody, upstreamModel); err != nil {
		return nil, err
	}
	resp, err := s.doOpenAIUpstream(upstreamReq, proxyURL, account)
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, true)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := s.readOpenAIUpstreamError(resp)
		if IsUpstreamContextRejection(resp.StatusCode, respBody) {
			writeGatewayContextRejection(c, resp.StatusCode)
			return nil, errGatewayContextRejection
		}
		if foErr := s.failoverOpenAIUpstreamHTTPError(ctx, c, account, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		writeChatCompletionsError(c, mapUpstreamStatusCode(resp.StatusCode), "server_error", upstreamMsg)
		return nil, fmt.Errorf("upstream error: %d %s", resp.StatusCode, upstreamMsg)
	}

	// Bill the Anthropic effort actually sent upstream (for example xhigh → max).
	reasoningEffort := nativeMessagesUsageEffort(account, forwardedBody, upstreamModel)

	if clientStream {
		return s.handleCCStreamingFromNativeAnthropic(resp, c, originalModel, billingModel, upstreamModel, reasoningEffort, startTime, account)
	}
	return s.handleCCBufferedFromNativeAnthropic(resp, c, originalModel, billingModel, upstreamModel, reasoningEffort, startTime, account)
}

// handleCCBufferedFromNativeAnthropic reads Anthropic SSE events, assembles the
// full response, then converts Anthropic → Responses → Chat Completions.
func (s *OpenAIGatewayService) handleCCBufferedFromNativeAnthropic(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
	accounts ...*Account,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	var account *Account
	if len(accounts) > 0 {
		account = accounts[0]
	}

	streamStatus := newAnthropicCompatStream(s.cfg, c, resp, "native anthropic adapter", requestID)
	defer streamStatus.stop()

	var finalResp *apicompat.AnthropicResponse
	var usage ClaudeUsage
	resultWithUsage := func() *OpenAIForwardResult {
		result := &OpenAIForwardResult{RequestID: requestID, UpstreamHeaders: resp.Header,
			Usage: claudeUsageToOpenAIUsage(&usage), UsageUnavailable: !streamStatus.usageSeen, Model: originalModel, BillingModel: billingModel,
			UpstreamModel: upstreamModel, UpstreamEndpoint: "/v1/messages", ReasoningEffort: reasoningEffort,
			Stream: false, ClientDisconnect: streamStatus.clientDisconnected, Duration: time.Since(startTime)}
		applyObservedUpstreamResponseModel(c, result)
		return result
	}

	// 读间隔上限：上游挂住 SSE 时中止组装（缓冲路径尚未提交响应头，可回 502）。
	for {
		event, payload, ok := streamStatus.nextEvent()
		if !ok {
			if !streamStatus.clientDisconnected {
				if resetErr, handled := handleOpenAIHTTP2PeerReset(c, account, requestID, streamStatus.readErr); handled {
					return resultWithUsage(), resetErr
				}
			}
			break
		}
		observeAnthropicUpstreamResponseModel(c, []byte(payload))

		if event.Type == "error" {
			return resultWithUsage(), writeNativeAnthropicSemanticFailure(c, []byte(payload), originalModel)
		}
		streamStatus.observe(&event, payload)
		if event.Type == "message_stop" {
			continue
		}

		if event.Type == "message_start" && event.Message != nil {
			finalResp = event.Message
			mergeAnthropicUsage(&usage, event.Message.Usage)
		}
		if event.Type == "message_delta" {
			if event.Usage != nil {
				mergeAnthropicUsage(&usage, *event.Usage)
			}
			if event.Delta != nil && event.Delta.StopReason != "" && finalResp != nil {
				finalResp.StopReason = apicompat.AnthropicStopReasonPtr(event.Delta.StopReason)
			}
		}
		if event.Type == "content_block_start" && event.ContentBlock != nil && finalResp != nil {
			finalResp.Content = append(finalResp.Content, *event.ContentBlock)
		}
		if event.Type == "content_block_delta" && event.Delta != nil && finalResp != nil && event.Index != nil {
			idx := *event.Index
			if idx >= 0 && idx < len(finalResp.Content) {
				switch event.Delta.Type {
				case "text_delta":
					finalResp.Content[idx].Text += event.Delta.Text
				case "thinking_delta":
					finalResp.Content[idx].Thinking += event.Delta.Thinking
				case "input_json_delta":
					finalResp.Content[idx].Input = appendRawJSON(finalResp.Content[idx].Input, event.Delta.PartialJSON)
				}
			}
		}
	}

	if failure := streamStatus.failure(); failure != nil {
		_ = resp.Body.Close()
		if !streamStatus.clientDisconnected {
			writeChatCompletionsError(c, http.StatusBadGateway, failure.errType, failure.message)
		}
		return resultWithUsage(), failure.err
	}

	if finalResp == nil {
		writeChatCompletionsError(c, http.StatusBadGateway, "server_error", "Upstream stream ended without a response")
		return nil, fmt.Errorf("upstream stream ended without response")
	}

	normalizeAnthropicEventUsageForResponses(&apicompat.AnthropicStreamEvent{Message: finalResp}, usage)

	responsesResp := apicompat.AnthropicToResponsesResponse(finalResp)
	ccResp := apicompat.ResponsesToChatCompletions(responsesResp, originalModel)

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	// 非流式响应必须是 application/json（上游被强制流式，透传头会污染）。
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	renderErrorsBefore := len(c.Errors)
	if respBytes, err := json.Marshal(ccResp); err == nil {
		respBytes = reverseToolNamesIfPresent(c, respBytes)
		c.Data(http.StatusOK, "application/json; charset=utf-8", respBytes)
	} else {
		c.JSON(http.StatusOK, ccResp)
	}

	result := &OpenAIForwardResult{
		RequestID:        requestID,
		UpstreamHeaders:  resp.Header,
		Usage:            claudeUsageToOpenAIUsage(&usage),
		UsageUnavailable: !streamStatus.usageSeen,
		Model:            originalModel,
		BillingModel:     billingModel,
		UpstreamModel:    upstreamModel,
		UpstreamEndpoint: "/v1/messages",
		ReasoningEffort:  reasoningEffort,
		Stream:           false,
		Duration:         time.Since(startTime),
	}
	applyObservedUpstreamResponseModel(c, result)
	if err := anthropicBufferedWriteFailure(c, streamStatus, renderErrorsBefore); err != nil {
		result.ClientDisconnect = true
		return result, err
	}
	return result, nil
}

// handleCCStreamingFromNativeAnthropic reads Anthropic SSE events, converts each
// to Responses events, then to Chat Completions chunks, and writes them.
func (s *OpenAIGatewayService) handleCCStreamingFromNativeAnthropic(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
	accounts ...*Account,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	var account *Account
	if len(accounts) > 0 {
		account = accounts[0]
	}

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	anthState := apicompat.NewAnthropicEventToResponsesState()
	anthState.Model = originalModel
	ccState := apicompat.NewResponsesEventToChatState()
	ccState.Model = originalModel

	var usage ClaudeUsage
	var firstTokenMs *int
	firstChunk := true

	streamStatus := newAnthropicCompatStream(s.cfg, c, resp, "native anthropic adapter", requestID)
	defer streamStatus.stop()

	resultWithUsage := func() *OpenAIForwardResult {
		result := &OpenAIForwardResult{
			RequestID:        requestID,
			UpstreamHeaders:  resp.Header,
			Usage:            claudeUsageToOpenAIUsage(&usage),
			UsageUnavailable: !streamStatus.usageSeen,
			Model:            originalModel,
			BillingModel:     billingModel,
			UpstreamModel:    upstreamModel,
			UpstreamEndpoint: "/v1/messages",
			ReasoningEffort:  reasoningEffort,
			Stream:           true,
			Duration:         time.Since(startTime),
			FirstTokenMs:     firstTokenMs,
			ClientDisconnect: streamStatus.clientDisconnected,
		}
		applyObservedUpstreamResponseModel(c, result)
		return result
	}

	// 读间隔上限：上游挂住 SSE（不发数据也不断连）时结束排水。上游 ctx 为
	// WithoutCancel 且 http.Client 无整体 Timeout，无此界限则客户端断开后
	// scanner.Scan() 永久阻塞（见 anthropic native pump 文件注释）。
	writeChunk := func(chunk apicompat.ChatCompletionsChunk) bool {
		if streamStatus.clientDisconnected {
			return false // 已断开：不再写客户端，只排水上游累计 usage
		}
		sse, err := apicompat.ChatChunkToSSE(chunk)
		if err != nil {
			return false
		}
		out := reverseToolNamesIfPresentString(c, sse)
		if _, err := fmt.Fprint(c.Writer, out); err != nil {
			streamStatus.markClientDisconnected()
			return true
		}
		return false
	}

	processAnthropicEvent := func(event *apicompat.AnthropicStreamEvent) bool {
		if firstChunk {
			firstChunk = false
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}

		// usage 恒累计（含客户端断开后的排水阶段，payg 上游照常计费）。
		if event.Type == "message_delta" && event.Usage != nil {
			mergeAnthropicUsage(&usage, *event.Usage)
		}
		if event.Type == "message_start" && event.Message != nil {
			mergeAnthropicUsage(&usage, event.Message.Usage)
		}

		// Keep the outward Responses/Chat usage on the same normalized buckets used
		// for billing, including converter handlers that consume event usage.
		syncAnthropicResponsesUsage(anthState, usage)
		normalizeAnthropicEventUsageForResponses(event, usage)

		// 客户端已断开：跳过转换与写出，继续读上游直到流结束（usage 完整、
		// 连接及时归还），不再提前 return。
		if streamStatus.clientDisconnected {
			return false
		}

		responsesEvents := apicompat.AnthropicEventToResponsesEvents(event, anthState)
		for _, resEvt := range responsesEvents {
			ccChunks := apicompat.ResponsesEventToChatChunks(&resEvt, ccState)
			for _, chunk := range ccChunks {
				if writeChunk(chunk) {
					return false
				}
			}
		}
		if len(responsesEvents) > 0 {
			if err := FlushGatewayResponse(c.Writer); err != nil {
				streamStatus.markClientDisconnected()
			}
		}
		return false
	}

	for {
		event, payload, ok := streamStatus.nextEvent()
		if !ok {
			if !streamStatus.clientDisconnected {
				if resetErr, handled := handleOpenAIHTTP2PeerReset(c, account, requestID, streamStatus.readErr); handled {
					return resultWithUsage(), resetErr
				}
			}
			break
		}
		observeAnthropicUpstreamResponseModel(c, []byte(payload))

		if event.Type == "error" {
			if streamStatus.clientDisconnected {
				return resultWithUsage(), errors.New("upstream semantic stream failure")
			}
			return resultWithUsage(), writeNativeAnthropicSemanticFailure(c, []byte(payload), originalModel)
		}
		streamStatus.observe(&event, payload)
		if event.Type == "message_stop" {
			continue
		}

		// Forward received usage regardless of the client stream_options.
		// The intermediate Responses converter synthesizes usage even when absent.
		ccState.IncludeUsage = ccState.IncludeUsage || anthropicChatStreamHasUsage(&event, payload)

		if processAnthropicEvent(&event) {
			return resultWithUsage(), nil
		}
	}

	if failure := streamStatus.failure(); failure != nil {
		_ = resp.Body.Close()
		if !streamStatus.clientDisconnected {
			writeAnthropicCompatCCStreamFailure(c, failure)
		}
		return resultWithUsage(), failure.err
	}

	// Finalize both state machines（客户端已断开时仍执行，保证 usage 汇总完整）。
	finalResEvents := apicompat.FinalizeAnthropicResponsesStream(anthState)
	for _, resEvt := range finalResEvents {
		ccChunks := apicompat.ResponsesEventToChatChunks(&resEvt, ccState)
		for _, chunk := range ccChunks {
			if writeChunk(chunk) {
				streamStatus.markClientDisconnected()
				return resultWithUsage(), streamStatus.failure().err
			}
		}
	}
	finalCCChunks := apicompat.FinalizeResponsesChatStream(ccState)
	for _, chunk := range finalCCChunks {
		if writeChunk(chunk) {
			streamStatus.markClientDisconnected()
			return resultWithUsage(), streamStatus.failure().err
		}
	}

	if !streamStatus.clientDisconnected {
		if _, err := fmt.Fprint(c.Writer, "data: [DONE]\n\n"); err != nil {
			streamStatus.markClientDisconnected()
			return resultWithUsage(), streamStatus.failure().err
		}
		if err := FlushGatewayResponse(c.Writer); err != nil {
			streamStatus.markClientDisconnected()
		}
	}

	if streamStatus.clientDisconnected {
		return resultWithUsage(), streamStatus.failure().err
	}
	return resultWithUsage(), nil
}

// Complete upstream error events are terminal semantic failures. The conversion state
// machines do not consume them, so stop before their successful finalizers.
func writeNativeAnthropicSemanticFailure(c *gin.Context, payload []byte, model string) error {
	message := sanitizeUpstreamErrorMessage(extractOpenAISSEErrorMessage(payload))
	if message == "" {
		message = "Upstream request failed"
	}
	errType := strings.TrimSpace(gjson.GetBytes(payload, "error.type").String())
	if errType == "" {
		errType = "upstream_error"
	}
	contextRejected := isOpenAIContextWindowError(message, payload)
	if contextRejected {
		// Some CC providers supply only the canonical code. Retain request
		// ownership/SLA exclusion even when error.type is absent or generic.
		errType = "invalid_request_error"
	}
	code := strings.TrimSpace(gjson.GetBytes(payload, "error.code").String())
	if code == "" {
		code = errType
	}
	status := openAIStreamFailureStatus(payload, message)
	if contextRejected {
		status = http.StatusBadRequest
	}
	body := gin.H{"error": gin.H{"type": errType, "code": code, "message": message}}
	if strings.HasSuffix(strings.TrimRight(c.Request.URL.Path, "/"), "/messages") {
		body["type"] = "error"
	}
	safeBody, _ := json.Marshal(body)
	if c.Writer.Written() {
		if contextRejected {
			// The code establishes a request rejection even if the provider's
			// message is generic. Do not inherit attempt attribution or SLA
			// ownership from the surrounding HTTP200 stream.
			MarkOpsStreamErrorValue(c, OpsStreamError{
				ErrType: errType, Code: code, Message: message, IntendedStatus: status,
				CountTowardsSLA: true, RequestScoped: true,
			})
		} else {
			MarkOpsStreamFailure(c, errType, code, message, status)
		}
		if strings.Contains(c.Request.URL.Path, "/responses") {
			_, _ = fmt.Fprint(c.Writer, buildOpenAIResponseFailedSSE("", model, time.Now().Unix(), safeBody, message))
		} else {
			_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", safeBody)
		}
		_ = FlushGatewayResponse(c.Writer)
	} else {
		c.Header("Content-Type", "application/json")
		c.Data(status, "application/json", safeBody)
	}
	MarkResponseCommitted(c)
	return errors.New("upstream semantic stream failure")
}
