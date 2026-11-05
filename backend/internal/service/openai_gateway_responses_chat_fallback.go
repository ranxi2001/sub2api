package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// forwardResponsesViaRawChatCompletions serves /v1/responses clients through an
// upstream that only supports /v1/chat/completions.
func (s *OpenAIGatewayService) forwardResponsesViaRawChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	startTime time.Time,
) (*OpenAIForwardResult, error) {

	// DeepSeek 原生 /responses 不认识 compaction_trigger：remote compaction v2 会得到
	// reasoning+message 而非 compaction item，Codex 判 fatal。这里改写成普通总结回合
	// （剥 trigger + 注入总结指令 + 强制非流式），回程再合成 compaction item。
	compact := isOpenAINativeCompactionV2(c) && HasCompactionTriggerInInput(body)
	if compact {
		rewritten, err := buildDeepSeekCompactChatBody(body)
		if err != nil {
			writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return nil, fmt.Errorf("build deepseek compact chat body: %w", err)
		}
		body = rewritten
		logger.L().Info("openai responses chat fallback: deepseek compact request rewritten",
			zap.Int64("account_id", account.ID),
			zap.Int("rewritten_body_bytes", len(body)),
		)
	}

	var responsesReq apicompat.ResponsesRequest
	if err := json.Unmarshal(body, &responsesReq); err != nil {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, fmt.Errorf("parse responses request: %w", err)
	}
	originalModel := strings.TrimSpace(responsesReq.Model)
	if originalModel == "" {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}

	clientStream := responsesReq.Stream
	// Codex omits reasoning.summary when configured with summary=none.
	// Only an explicit summary request enables plaintext summaries.
	suppressSummary := responsesReq.Reasoning == nil || strings.TrimSpace(responsesReq.Reasoning.Summary) == "" || strings.EqualFold(strings.TrimSpace(responsesReq.Reasoning.Summary), "none")
	// custom 工具（如 codex 的 exec）降级为 function 工具转发，回程需按名字还原为
	// custom_tool_call 项，先记下名字集合；tool_search 工具同理，回程还原为
	// tool_search_call 项；namespace 子工具（如 MCP 工具）摊平转发，回程按映射还原
	// 为带 namespace 字段的 function_call 项。
	effectiveTools, err := apicompat.EffectiveResponsesTools(&responsesReq)
	if err != nil {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("resolve responses tools: %w", err)
	}
	customTools := apicompat.CustomToolNames(effectiveTools)
	functionTools := apicompat.FunctionToolNames(effectiveTools)
	toolSearch := apicompat.HasToolSearchTool(effectiveTools)
	namespaceTools := apicompat.NamespaceToolNames(effectiveTools)

	// 自愈回写：历史里带明文 summary 的 reasoning item 刷新进缓存，覆盖 Redis
	// 被 flush / 跨实例漂移后同 id 的 encrypted-only 副本无法再取明文的情况。
	s.recacheReasoningItemsFromInput(responsesReq.Input)

	chatReq, err := apicompat.ResponsesToChatCompletionsRequestWithOptions(&responsesReq, &apicompat.ResponsesToChatOptions{
		ReasoningContentByID: s.reasoningContentByID,
	})
	if err != nil {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("convert responses to chat completions: %w", err)
	}

	billingModel := resolveOpenAIForwardModel(account, originalModel, "")
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	if err := validateGPT61SolCompatRequest(body, upstreamModel); err != nil {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	if openai.RequiresResponsesForToolCalls(upstreamModel) && len(effectiveTools) > 0 {
		err := fmt.Errorf("%s requires Responses for tool calls; this account only supports Chat Completions", normalizeKnownOpenAICodexModel(upstreamModel))
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	reasoningEffort := extractOpenAIReasoningEffortFromBody(body, upstreamModel, billingModel, originalModel)
	// 国产模型默认 effort 补充：需要 mappedModel 判定，推迟到 billingModel 算出之后。
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, body, billingModel)
	chatReq.Model = upstreamModel
	if clientStream {
		chatReq.StreamOptions = &apicompat.ChatStreamOptions{IncludeUsage: true}
	}

	chatBody, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal chat completions fallback request: %w", err)
	}
	chatBody, err = s.applyOpenAIFastPolicyToBody(ctx, account, upstreamModel, chatBody)
	if err != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(err, &blocked) {
			writeOpenAIFastPolicyBlockedResponse(c, blocked)
		}
		return nil, err
	}
	// /v1/responses 降级到 raw CC 的出站与 forwardAsRawChatCompletions 共用同一个
	// 独立 Ollama Cloud token 钩子；chatReq.Model 已是模型映射后的 upstreamModel。
	chatBody = clampOllamaCloudUpstreamMaxTokens(account, chatBody)
	// DeepSeek 的 /chat/completions 不接受 response_format=json_schema（400
	// "This response_format type is unavailable now"）。Responses 的 text.format
	// json_schema 经 chat 桥原样转成该字段，必须剔除；text / json_object 均可用，
	// 保持原样。剔除后模型退回纯文本输出，Codex 不依赖结构化输出即可继续。
	chatBody = stripDeepSeekUnsupportedChatResponseFormat(account, chatBody)
	chatBody, err = normalizeMistralChatRequest(account, chatBody)
	if err != nil {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	if account.Platform == PlatformMistral {
		reasoningEffort = extractOpenAIReasoningEffortFromBody(chatBody, upstreamModel, billingModel, originalModel)
		reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, chatBody, upstreamModel)
	}
	// Keep the final outbound tier for usage-time reconciliation. A policy
	// filter that removes the field therefore leaves this nil.
	serviceTier := extractOpenAIServiceTierFromBody(chatBody)

	logger.L().Debug("openai responses: forwarding via raw chat completions",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)
	SetOpsUpstreamModel(c, upstreamModel)

	// Build and send upstream request via the shared CC pipeline
	apiKey, targetURL, err := s.resolveCCFallbackTarget(account)
	if err != nil {
		return nil, err
	}
	if _, err := s.admitOpenAITurn(ctx, c, account, upstreamModel); err != nil {
		return nil, err
	}
	resp, err := s.sendCCUpstreamRequest(ctx, c, account, targetURL, chatBody, clientStream, apiKey, account.GetOpenAIUserAgent(), "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := s.readOpenAIUpstreamError(resp)
		if compact {
			logger.L().Warn("openai responses chat fallback: deepseek compact upstream error",
				zap.Int64("account_id", account.ID),
				zap.Int("status", resp.StatusCode),
				zap.String("message", upstreamMsg),
			)
		}
		if foErr := s.failoverOpenAIUpstreamHTTPError(ctx, c, account, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		return s.handleErrorResponse(ctx, resp, c, account, chatBody, billingModel)
	}

	var toolTextDecoder *apicompat.DeepSeekToolTextDecoder
	if !compact && len(chatReq.Tools) > 0 && isDeepSeekSemanticsChatUpstream(account, upstreamModel) {
		toolTextDecoder = apicompat.NewDeepSeekToolTextDecoder(chatReq.Tools)
	}
	if clientStream {
		return s.streamChatCompletionsAsResponses(c, resp, account, originalModel, customTools, functionTools, toolSearch, namespaceTools, billingModel, upstreamModel, reasoningEffort, serviceTier, suppressSummary, startTime, toolTextDecoder)
	}
	return s.bufferChatCompletionsAsResponses(c, resp, account, originalModel, customTools, functionTools, toolSearch, namespaceTools, billingModel, upstreamModel, reasoningEffort, serviceTier, suppressSummary, startTime, compact, toolTextDecoder)
}

func (s *OpenAIGatewayService) bufferChatCompletionsAsResponses(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	originalModel string,
	customTools map[string]bool,
	functionTools map[string]bool,
	toolSearch bool,
	namespaceTools map[string]apicompat.NamespacedToolName,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	suppressSummary bool,
	startTime time.Time,
	compact bool,
	toolTextDecoder *apicompat.DeepSeekToolTextDecoder,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	ccResp, usage, err := s.readCCUpstreamJSONResponse(c, resp, account, writeOpenAIResponsesFallbackError)
	if err != nil {
		return nil, err
	}
	var toolTextErr error
	if toolTextDecoder != nil && len(ccResp.Choices) > 0 {
		toolTextErr = toolTextDecoder.RecoverMessage(&ccResp.Choices[0].Message, ccResp.Choices[0].FinishReason)
	}
	responsesResp := apicompat.ChatCompletionsResponseToResponses(ccResp, originalModel, customTools, functionTools, toolSearch, namespaceTools)
	if toolTextErr != nil {
		responsesResp.Status = "failed"
		responsesResp.IncompleteDetails = nil
		responsesResp.Output = []apicompat.ResponsesOutput{}
		responsesResp.Error = deepSeekToolTextError(c, requestID, toolTextErr)
	}
	if responsesResp.Status != "completed" {
		usage.CacheReadKnown = false
	}
	recordChatReasoningOnlyFailure(c, requestID, upstreamModel, responsesResp)
	s.cacheReasoningItemsFromOutput(responsesResp.Output)
	if suppressSummary {
		responsesResp.Output = withoutReasoningSummaries(responsesResp.Output)
	}

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	if compact && responsesResp.Status == "failed" {
		// A failed summary must not become a successful empty compaction item.
		sse, err := apicompat.ResponsesEventToSSE(apicompat.ResponsesStreamEvent{Type: "response.failed", SequenceNumber: 1, Response: responsesResp})
		if err != nil {
			return nil, fmt.Errorf("marshal compact failure: %w", err)
		}
		c.Data(http.StatusOK, "text/event-stream", []byte(sse+"data: [DONE]\n\n"))
	} else if compact {
		summary := compactSummaryTextFromResponses(responsesResp.Output)
		logger.L().Info("openai responses chat fallback: deepseek compact synthesizing",
			zap.Int("upstream_output_items", len(responsesResp.Output)),
			zap.Int("summary_len", len(summary)),
		)
		compactResp := buildDeepSeekCompactResponse(responsesResp, summary)
		encoded, err := json.Marshal(compactResp)
		if err != nil {
			return nil, fmt.Errorf("marshal deepseek compact response: %w", err)
		}
		payload, ok := buildDeepSeekCompactSSEPayload(encoded)
		if !ok {
			return nil, fmt.Errorf("build deepseek compact SSE payload")
		}
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Writer.WriteHeader(http.StatusOK)
		if _, err := c.Writer.Write(payload); err != nil {
			return nil, err
		}
		c.Writer.Flush()
	} else {
		c.JSON(http.StatusOK, responsesResp)
	}

	return &OpenAIForwardResult{
		RequestID:                     requestID,
		UpstreamHeaders:               resp.Header,
		Usage:                         usage,
		Model:                         originalModel,
		BillingModel:                  billingModel,
		UpstreamModel:                 upstreamModel,
		ReasoningEffort:               reasoningEffort,
		UpstreamResponseModel:         observedUpstreamResponseModel(c),
		UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
		UpstreamResponseServiceTier:   observedUpstreamResponseServiceTier(c),
		ServiceTier:                   resolvedOpenAIUpstreamServiceTier(c, serviceTier),
		Stream:                        false,
		Duration:                      time.Since(startTime),
	}, nil
}

func (s *OpenAIGatewayService) streamChatCompletionsAsResponses(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	originalModel string,
	customTools map[string]bool,
	functionTools map[string]bool,
	toolSearch bool,
	namespaceTools map[string]apicompat.NamespacedToolName,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	suppressSummary bool,
	startTime time.Time,
	toolTextDecoder *apicompat.DeepSeekToolTextDecoder,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := s.newStreamHeaderWriter(c, resp.Header)

	state := apicompat.NewChatCompletionsToResponsesStreamState(originalModel)
	state.CustomTools = customTools
	state.FunctionTools = functionTools
	state.ToolSearchDeclared = toolSearch
	state.NamespaceTools = namespaceTools
	clientDisconnected := false

	writeEvents := func(events []apicompat.ResponsesStreamEvent) {
		// Cache the original completed reasoning items before this write boundary;
		// clients can return the opaque item ID for DeepSeek tool-history replay.
		if suppressSummary {
			events = withoutReasoningSummaryEvents(events)
		}
		if clientDisconnected || len(events) == 0 {
			return
		}
		writeStreamHeaders()
		for _, event := range events {
			sse, err := apicompat.ResponsesEventToSSE(event)
			if err != nil {
				logger.L().Warn("openai responses chat fallback: failed to marshal stream event",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				continue
			}
			if _, err := fmt.Fprint(c.Writer, sse); err != nil {
				clientDisconnected = true
				logger.L().Debug("openai responses chat fallback: client disconnected, continuing to drain upstream for billing",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				return
			}
		}
		c.Writer.Flush()
	}

	scan := s.scanCCStream(c, resp, account, "openai responses chat fallback", requestID, startTime, func(chunk *apicompat.ChatCompletionsChunk) {
		if toolTextDecoder != nil {
			chunk = toolTextDecoder.Push(chunk)
		}
		events := apicompat.ChatCompletionsChunkToResponsesEvents(chunk, state)
		s.cacheReasoningItemsFromEvents(events)
		writeEvents(events)
	})

	if scan.Err != nil {
		return &OpenAIForwardResult{
			RequestID:                     requestID,
			UpstreamHeaders:               resp.Header,
			Usage:                         scan.Usage,
			Model:                         originalModel,
			BillingModel:                  billingModel,
			UpstreamModel:                 upstreamModel,
			ReasoningEffort:               reasoningEffort,
			UpstreamResponseModel:         observedUpstreamResponseModel(c),
			UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
			UpstreamResponseServiceTier:   observedUpstreamResponseServiceTier(c),
			ServiceTier:                   resolvedOpenAIUpstreamServiceTier(c, serviceTier),
			Stream:                        true,
			ClientDisconnect:              clientDisconnected,
			Duration:                      time.Since(startTime),
			FirstTokenMs:                  scan.FirstTokenMs,
		}, fmt.Errorf("stream usage incomplete: %w", scan.Err)
	}
	if toolTextDecoder != nil {
		chunk, err := toolTextDecoder.Finish(state.FinishReason)
		if err != nil {
			state.UpstreamError = deepSeekToolTextError(c, requestID, err)
		} else if chunk != nil {
			events := apicompat.ChatCompletionsChunkToResponsesEvents(chunk, state)
			s.cacheReasoningItemsFromEvents(events)
			writeEvents(events)
		}
	}
	if err := state.ValidateToolCallArguments(); state.UpstreamError == nil && err != nil {
		return &OpenAIForwardResult{
			RequestID:                     requestID,
			UpstreamHeaders:               resp.Header,
			Usage:                         scan.Usage,
			Model:                         originalModel,
			BillingModel:                  billingModel,
			UpstreamModel:                 upstreamModel,
			ReasoningEffort:               reasoningEffort,
			UpstreamResponseModel:         observedUpstreamResponseModel(c),
			UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
			UpstreamResponseServiceTier:   observedUpstreamResponseServiceTier(c),
			ServiceTier:                   resolvedOpenAIUpstreamServiceTier(c, serviceTier),
			Stream:                        true,
			ClientDisconnect:              clientDisconnected,
			Duration:                      time.Since(startTime),
			FirstTokenMs:                  scan.FirstTokenMs,
		}, fmt.Errorf("invalid tool call arguments from upstream: %w", err)
	}

	finalEvents := apicompat.FinalizeChatCompletionsResponsesStream(state)
	protectionFailed := false
	for _, event := range finalEvents {
		if event.Type == "response.failed" {
			protectionFailed = true
			recordChatReasoningOnlyFailure(c, requestID, upstreamModel, event.Response)
		}
	}
	s.cacheReasoningItemsFromEvents(finalEvents)
	writeEvents(finalEvents)
	if !clientDisconnected {
		writeStreamHeaders()
		if _, err := fmt.Fprint(c.Writer, "data: [DONE]\n\n"); err != nil {
			clientDisconnected = true
		}
		if !clientDisconnected {
			c.Writer.Flush()
		}
	}
	if !scan.SawDone {
		logCCStreamMissingDoneSentinel("openai responses chat fallback", requestID)
	}
	if !scan.SawDone || clientDisconnected || protectionFailed {
		scan.Usage.CacheReadKnown = false
	}

	return &OpenAIForwardResult{
		RequestID:                     requestID,
		UpstreamHeaders:               resp.Header,
		Usage:                         scan.Usage,
		Model:                         originalModel,
		BillingModel:                  billingModel,
		UpstreamModel:                 upstreamModel,
		ReasoningEffort:               reasoningEffort,
		UpstreamResponseModel:         observedUpstreamResponseModel(c),
		UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
		UpstreamResponseServiceTier:   observedUpstreamResponseServiceTier(c),
		ServiceTier:                   resolvedOpenAIUpstreamServiceTier(c, serviceTier),
		Stream:                        true,
		ClientDisconnect:              clientDisconnected,
		Duration:                      time.Since(startTime),
		FirstTokenMs:                  scan.FirstTokenMs,
	}, nil
}

func chatChunkStartsResponsesOutput(chunk *apicompat.ChatCompletionsChunk) bool {
	if chunk == nil {
		return false
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != nil || choice.Delta.ReasoningContent != nil || len(choice.Delta.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

func recordChatReasoningOnlyFailure(c *gin.Context, requestID, model string, response *apicompat.ResponsesResponse) {
	if response == nil || response.Error == nil || response.Error.Code != "upstream_reasoning_only" {
		return
	}
	setOpsUpstreamError(c, http.StatusOK, response.Error.Code, response.Error.Message)
	logger.L().Warn("openai.responses_reasoning_only",
		zap.String("request_id", requestID), zap.String("upstream_model", model),
		zap.String("error_code", response.Error.Code))
}

// responsesReasoningCacheTTL 是 reasoning 缓存（按 reasoning item id）的过期时间。
// Codex 会话可能跨多天恢复历史，取 7 天。
const responsesReasoningCacheTTL = 7 * 24 * time.Hour

// reasoningContentByID 按 reasoning item id 回查缓存的 reasoning 全文，供
// Responses→CC 桥接在客户端不回传明文 summary（encrypted-only reasoning
// item）时回注 reasoning_content。任何失败都 fail-open 返回 ""（维持桥接原
// 行为），因为缓存只是优化而非正确性前提。
func (s *OpenAIGatewayService) reasoningContentByID(itemID string) string {
	if s == nil || s.cache == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	content, err := s.cache.GetReasoningContent(ctx, itemID)
	if err != nil {
		return ""
	}
	return content
}

// recacheReasoningItemsFromInput 把请求历史里带明文 summary 的 reasoning item
// 重新写入缓存（best-effort）。Codex 多数时候会原样回传明文 summary，借机
// 刷新 TTL 并自愈 Redis 被 flush / 跨实例漂移造成的缓存缺失。
func (s *OpenAIGatewayService) recacheReasoningItemsFromInput(inputRaw json.RawMessage) {
	if s == nil || s.cache == nil {
		return
	}
	inputRaw = bytes.TrimSpace(inputRaw)
	if len(inputRaw) == 0 || inputRaw[0] != '[' {
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(inputRaw, &items); err != nil {
		return
	}
	for _, raw := range items {
		id, text, ok := apicompat.ExtractResponsesReasoningItem(raw)
		if !ok || id == "" || text == "" {
			continue
		}
		s.setReasoningContent(id, text)
	}
}

// cacheReasoningItemsFromEvents 从 Responses 流事件里提取完成的 reasoning
// item 写入缓存（覆盖一个流中的多个 reasoning item）。
func (s *OpenAIGatewayService) cacheReasoningItemsFromEvents(events []apicompat.ResponsesStreamEvent) {
	for _, event := range events {
		if event.Type != "response.output_item.done" || event.Item == nil {
			continue
		}
		s.cacheReasoningItem(event.Item)
	}
}

// cacheReasoningItemsFromOutput 从非流式 Responses 响应的 output 里提取
// reasoning item 写入缓存。
func (s *OpenAIGatewayService) cacheReasoningItemsFromOutput(output []apicompat.ResponsesOutput) {
	for i := range output {
		s.cacheReasoningItem(&output[i])
	}
}

func (s *OpenAIGatewayService) cacheReasoningItem(item *apicompat.ResponsesOutput) {
	if item == nil || item.Type != "reasoning" || item.ID == "" {
		return
	}
	var parts []string
	for _, sum := range item.Summary {
		if t := strings.TrimSpace(sum.Text); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return
	}
	s.setReasoningContent(item.ID, strings.Join(parts, "\n"))
}

// setReasoningContent 写入缓存，使用 detached ctx：客户端断连后仍在 drain
// 上游流（计费需要），此时的 reasoning 也是后续轮次回注所依赖的，不能随
// 请求 ctx 一起取消。失败仅记日志，不影响转发。
func (s *OpenAIGatewayService) setReasoningContent(itemID, content string) {
	if s == nil || s.cache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.cache.SetReasoningContent(ctx, itemID, content, responsesReasoningCacheTTL); err != nil {
		logger.L().Warn("openai responses chat fallback: cache reasoning content failed",
			zap.Error(err),
			zap.String("item_id", itemID),
		)
	}
}

// stripDeepSeekUnsupportedChatResponseFormat 剔除 DeepSeek /chat/completions 不接受的
// response_format。Responses 的 text.format=json_schema 经 chat 桥会被原样转成
// response_format:{"type":"json_schema"}，而 DeepSeek 会直接 400
// （This response_format type is unavailable now）。DeepSeek 接受 text 与 json_object，
// 仅 json_schema 需移除；移除后模型退回纯文本输出，Codex 不依赖结构化输出即可继续。
func stripDeepSeekUnsupportedChatResponseFormat(account *Account, chatBody []byte) []byte {
	if !isDeepSeekSemanticsChatUpstream(account, gjson.GetBytes(chatBody, "model").String()) {
		return chatBody
	}
	if strings.TrimSpace(gjson.GetBytes(chatBody, "response_format.type").String()) != "json_schema" {
		return chatBody
	}
	updated, err := sjson.DeleteBytes(chatBody, "response_format")
	if err != nil {
		return chatBody
	}
	return updated
}

// isDeepSeekSemanticsChatUpstream 报告出站 /chat/completions 请求是否打到
// 不接受 response_format=json_schema 的 DeepSeek 语义上游。
//
// 两条判据任一成立即可：账号本身就是 DeepSeek 上游（platform=deepseek 或
// base_url 指向 api.deepseek.com），或者出站模型名属于 DeepSeek 模型族
// （聚合站场景：账号是 platform=openai，但本条请求已映射到 deepseek-*，
// chatReq.Model 在进入本函数前已经改写为出站模型名）。
func isDeepSeekSemanticsChatUpstream(account *Account, upstreamModel string) bool {
	if account == nil {
		return false
	}
	if account.Platform == PlatformDeepseek {
		return true
	}
	if isDeepSeekAPIHost(account.GetOpenAIBaseURL()) {
		return true
	}
	return isDeepSeekModelName(upstreamModel)
}

// deepSeekChatReasoningPlaceholderText 是 Chat Completions 侧 thinking-mode
// 占位明文。必须是单个空格：DeepSeek 拒绝空串，非空即可通过；LiteLLM 同样注入
// 单个空格。
const deepSeekChatReasoningPlaceholderText = " "

// targetsOpenCodeZenUpstream 报告该账号上游是否是官方 OpenCode Zen / Go 网关。
// platform=opencode_go 直接命中；其他平台按 base_url 主机判定，覆盖用 API Key
// 直接把 openai 平台账号指向 opencode.ai 的接入方式。
func targetsOpenCodeZenUpstream(account *Account) bool {
	if account == nil {
		return false
	}
	if account.IsOpenCodeGo() {
		return true
	}
	return isOfficialOpenCodeHost(account.GetOpenAIBaseURL())
}

// isDeepSeekCatalogModel 判定（剥掉 opencode 前缀后的）模型 ID 是否属于
// DeepSeek 系列，例如 deepseek-v4-flash / deepseek-v4.1-flash / deepseek-chat。
func isDeepSeekCatalogModel(model string) bool {
	return strings.HasPrefix(normalizeOpenCodeGoModelID(model), "deepseek")
}

// requiresDeepSeekChatReasoning 报告该 Chat Completions 请求的上游是否按
// DeepSeek thinking mode 语义校验 reasoning_content。
//
// 保留 fork 按实际出站模型识别 DeepSeek 聚合网关的兼容路径，同时为官方
// OpenCode Zen / Go 的带前缀 DeepSeek 模型补充识别。
func requiresDeepSeekChatReasoning(account *Account, body []byte) bool {
	if isDeepSeekSemanticsChatUpstream(account, gjson.GetBytes(body, "model").String()) {
		return true
	}
	if !targetsOpenCodeZenUpstream(account) {
		return false
	}
	return isDeepSeekCatalogModel(gjson.GetBytes(body, "model").String())
}

// ensureDeepSeekChatReasoningPlaceholders 给缺 reasoning_content 的 assistant
// 消息补单个空格占位。DeepSeek thinking mode 要求历史里每条产生过思维的
// assistant 消息都回传该字段，否则 400
// "The `reasoning_content` in the thinking mode must be passed back to the API"。
//
// 桥接会从 summary / 缓存回注真实明文；这里只填仍为空的缺口，不覆盖已有内容。
// 判定见 requiresDeepSeekChatReasoning，不命中的上游原样返回（字节不变）。
func ensureDeepSeekChatReasoningPlaceholders(account *Account, body []byte) []byte {
	if !requiresDeepSeekChatReasoning(account, body) {
		return body
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	updated := body
	changed := false
	for i, msg := range messages.Array() {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}
		if msg.Get("reasoning_content").String() != "" {
			continue
		}
		next, err := sjson.SetBytes(updated, "messages."+strconv.Itoa(i)+".reasoning_content", deepSeekChatReasoningPlaceholderText)
		if err != nil {
			return body
		}
		updated = next
		changed = true
	}
	if !changed {
		return body
	}
	return updated
}

// Never include upstream markup, arguments, or credentials in protocol errors.
func deepSeekToolTextError(c *gin.Context, requestID string, err error) *apicompat.ResponsesError {
	const code = "upstream_tool_protocol_error"
	const message = "DeepSeek returned malformed text tool calls; no text tool call was executed. Retry with a fresh turn."
	setOpsUpstreamError(c, http.StatusOK, code, message)
	logger.L().Warn("openai.responses_tool_protocol_error", zap.String("request_id", requestID), zap.Error(err))
	return &apicompat.ResponsesError{Code: code, Message: message}
}
