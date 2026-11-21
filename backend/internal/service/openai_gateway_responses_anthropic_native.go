package service

// 国产供应商 Anthropic 协议账号的 Responses 入站反向路径。
//
// 客户端说 OpenAI Responses（/v1/responses，Codex 等）、上游是供应商原生
// Anthropic 端点（api_protocol=anthropic）时的交叉组合：请求 Responses→Anthropic
// 单次转换，响应 Anthropic 事件→Responses 事件转换。转换链与 Anthropic 平台的
// gateway_forward_as_responses.go 完全一致（复用同一组 apicompat 状态机），仅上游
// 发送/错误处理对齐 OpenAI 网关语义（模型映射、failover、transport error）。

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

// forwardResponsesViaNativeAnthropic serves OpenAI /v1/responses clients through
// a CN provider's native Anthropic endpoint.
//
// Conversion chain:
//
//	Request:  Responses → Anthropic (single conversion)
//	Response: Anthropic events → Responses events (stream state machine)
func (s *OpenAIGatewayService) forwardResponsesViaNativeAnthropic(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()
	SetActualOpenAIUpstreamEndpoint(c, "/v1/messages")

	// 1. Lower Codex client-side tools to function tools understood by Anthropic.
	adaptedBody, clientToolMapping, err := adaptResponsesClientToolsForAnthropic(body)
	if err != nil {
		writeResponsesError(c, http.StatusBadRequest, "invalid_request_error", "Failed to adapt request tools")
		return nil, fmt.Errorf("adapt responses client tools: %w", err)
	}

	// 2. Parse Responses request
	var responsesReq apicompat.ResponsesRequest
	if err := json.Unmarshal(adaptedBody, &responsesReq); err != nil {
		writeResponsesError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, fmt.Errorf("parse responses request: %w", err)
	}
	originalModel := responsesReq.Model
	if strings.TrimSpace(originalModel) == "" {
		writeResponsesError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	clientStream := responsesReq.Stream

	// 3. Convert Responses → Anthropic
	// Resolve the mapped model before choosing its thinking/tool protocol.
	billingModel := resolveOpenAIForwardModel(account, originalModel, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	if err := validateClaude55Request(body, upstreamModel); err != nil {
		writeResponsesError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	responsesReq.Model = upstreamModel
	anthropicReq, err := apicompat.ResponsesToAnthropicRequestWithOptions(&responsesReq, apicompat.ResponsesToAnthropicOptions{StrictMedia: true})
	if err != nil {
		writeResponsesError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("convert responses to anthropic: %w", err)
	}

	// 5. Force upstream streaming（客户端原始终决定响应格式；
	// 上游恒为流式，非流式由缓冲路径组装）。
	anthropicReq.Stream = true
	reqStream := true

	logger.L().Debug("openai responses: forwarding via native anthropic endpoint",
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
	// Bill the Anthropic effort actually sent upstream (for example xhigh → max).
	reasoningEffort := NormalizeClaudeOutputEffort(gjson.GetBytes(forwardedBody, "output_config.effort").String())
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, forwardedBody, upstreamModel)

	if _, err := s.admitOpenAITurn(ctx, c, account, upstreamModel, forwardedBody); err != nil {
		return nil, err
	}
	resp, err := s.doOpenAIUpstream(upstreamReq, proxyURL, account)
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, true)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := s.readOpenAIUpstreamError(resp)
		if foErr := s.failoverOpenAIUpstreamHTTPError(ctx, c, account, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		writeResponsesError(c, mapUpstreamStatusCode(resp.StatusCode), "server_error", upstreamMsg)
		return nil, fmt.Errorf("upstream error: %d %s", resp.StatusCode, upstreamMsg)
	}

	if clientStream {
		return s.handleResponsesStreamingFromNativeAnthropic(resp, c, originalModel, billingModel, upstreamModel, reasoningEffort, startTime, clientToolMapping, account)
	}
	return s.handleResponsesBufferedFromNativeAnthropic(resp, c, originalModel, billingModel, upstreamModel, reasoningEffort, startTime, clientToolMapping, account)
}

// handleResponsesBufferedFromNativeAnthropic reads Anthropic SSE events, assembles
// the full response, then converts Anthropic → Responses.
func (s *OpenAIGatewayService) handleResponsesBufferedFromNativeAnthropic(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
	clientToolMapping apicompat.ResponsesClientToolMapping,
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
				case "signature_delta":
					finalResp.Content[idx].Signature += event.Delta.Signature
				case "input_json_delta":
					finalResp.Content[idx].Input = appendRawJSON(finalResp.Content[idx].Input, event.Delta.PartialJSON)
				}
			}
		}
	}

	if failure := streamStatus.failure(); failure != nil {
		_ = resp.Body.Close()
		if !streamStatus.clientDisconnected {
			writeResponsesError(c, http.StatusBadGateway, failure.errType, failure.message)
		}
		return resultWithUsage(), failure.err
	}

	if finalResp == nil {
		writeResponsesError(c, http.StatusBadGateway, "server_error", "Upstream stream ended without a response")
		return nil, fmt.Errorf("upstream stream ended without response")
	}

	normalizeAnthropicEventUsageForResponses(&apicompat.AnthropicStreamEvent{Message: finalResp}, usage)

	if isClaude55SignedThinkingModel(upstreamModel) {
		finalResp.Model = upstreamModel
	}
	responsesResp := apicompat.AnthropicToResponsesResponse(finalResp)
	responsesResp.Model = originalModel

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	// 非流式响应必须是 application/json（上游被强制流式，透传头会污染）。
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	renderErrorsBefore := len(c.Errors)
	if respBytes, err := json.Marshal(responsesResp); err == nil {
		respBytes = reverseToolNamesIfPresent(c, respBytes)
		respBytes, _, err = apicompat.RestoreResponsesClientToolPayload(respBytes, clientToolMapping)
		if err != nil {
			return nil, fmt.Errorf("restore responses client tools: %w", err)
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", respBytes)
	} else {
		c.JSON(http.StatusOK, responsesResp)
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

// handleResponsesStreamingFromNativeAnthropic reads Anthropic SSE events, converts
// each to Responses SSE events, and writes them to the client.
func (s *OpenAIGatewayService) handleResponsesStreamingFromNativeAnthropic(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
	clientToolMapping apicompat.ResponsesClientToolMapping,
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

	state := apicompat.NewAnthropicEventToResponsesState()
	state.Model = originalModel
	state.PreserveThinkingSignatures = isClaude55SignedThinkingModel(upstreamModel)
	clientToolRestorer := apicompat.NewResponsesClientToolStreamRestorer(clientToolMapping)

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

	// 读间隔上限：上游挂住 SSE（不发数据也不断连）时结束转换循环。上游 ctx 为
	// WithoutCancel 且 http.Client 无整体 Timeout，无此界限则 scanner.Scan()
	// 永久阻塞（见 anthropic native pump 文件注释）。
	processAnthropicEvent := func(event *apicompat.AnthropicStreamEvent) {
		if firstChunk {
			firstChunk = false
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}

		if event.Type == "message_delta" && event.Usage != nil {
			mergeAnthropicUsage(&usage, *event.Usage)
		}
		if event.Type == "message_start" && event.Message != nil {
			mergeAnthropicUsage(&usage, event.Message.Usage)
		}

		// Keep terminal Responses usage aligned with the normalized billing
		// buckets. Normalize converter input too so raw overlapping totals cannot
		// overwrite the state when message_start/message_delta handlers run.
		syncAnthropicResponsesUsage(state, usage)
		normalizeAnthropicEventUsageForResponses(event, usage)

		events := apicompat.AnthropicEventToResponsesEvents(event, state)
		if streamStatus.clientDisconnected {
			return
		}
		for _, evt := range events {
			payload, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			payload = reverseToolNamesIfPresent(c, payload)
			payloads, _, err := clientToolRestorer.RestoreEvent(payload)
			if err != nil {
				continue
			}
			for _, restored := range payloads {
				eventType := gjson.GetBytes(restored, "type").String()
				if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", eventType, restored); err != nil {
					streamStatus.markClientDisconnected()
					return
				}
			}
		}
		if len(events) > 0 {
			if err := FlushGatewayResponse(c.Writer); err != nil {
				streamStatus.markClientDisconnected()
			}
		}
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

		processAnthropicEvent(&event)
	}

	if failure := streamStatus.failure(); failure != nil {
		_ = resp.Body.Close()
		if !streamStatus.clientDisconnected {
			writeAnthropicCompatResponsesStreamFailure(c, state, clientToolRestorer, failure)
		}
		return resultWithUsage(), failure.err
	}

	// Finalize state machine（客户端已断开时仍推进，保证 usage 汇总完整；仅在
	// 客户端仍连接时写出）。终态帧与逐事件路径一致过工具名反转与客户端工具还原，
	// 避免流截断时终态帧携带改写后的工具名。
	if finalEvents := apicompat.FinalizeAnthropicResponsesStream(state); len(finalEvents) > 0 && !streamStatus.clientDisconnected {
		wrote := false
		for _, evt := range finalEvents {
			payload, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			payload = reverseToolNamesIfPresent(c, payload)
			payloads, _, err := clientToolRestorer.RestoreEvent(payload)
			if err != nil {
				continue
			}
			for _, restored := range payloads {
				eventType := gjson.GetBytes(restored, "type").String()
				if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", eventType, restored); err != nil {
					streamStatus.markClientDisconnected()
					break
				}
				wrote = true
			}
			if streamStatus.clientDisconnected {
				break
			}
		}
		if wrote {
			if err := FlushGatewayResponse(c.Writer); err != nil {
				streamStatus.markClientDisconnected()
			}
		}
	}

	if streamStatus.clientDisconnected {
		return resultWithUsage(), streamStatus.failure().err
	}

	return resultWithUsage(), nil
}
