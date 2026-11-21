package service

// 国产供应商（kimi/zhipu/deepseek）原生 Anthropic 端点直通路径。
//
// 当账号 credentials["api_protocol"] = "anthropic" 时，入站 /v1/messages 请求
// 不再做 Anthropic→CC→Anthropic 双重转换，而是零转换直通供应商的官方
// Anthropic 兼容端点（如 https://open.bigmodel.cn/api/anthropic/v1/messages），
// 适配 Claude Code 等原生 Anthropic 客户端。转发骨架以
// gateway_anthropic_passthrough.go 的 APIKey 透传为模板（字节级 SSE 中继 +
// usage 解析），错误/failover 语义对齐 OpenAI 网关其他路径
// （failoverOpenAIUpstreamHTTPError / handleAnthropicErrorResponse）。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// forwardAnthropicViaNativeAnthropicEndpoint 将 Anthropic Messages 请求零转换
// 直通到国产供应商的原生 Anthropic 端点。仅做模型名映射与少量 body 清洗
// （空文本块 / web-search 历史块），协议本身不转换。对 DeepSeek 官方账号补齐
// tool_use 历史的单个空格占位 thinking 块（thinking 模式下缺 thinking 会被上游 400）。
func (s *OpenAIGatewayService) forwardAnthropicViaNativeAnthropicEndpoint(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()
	SetActualOpenAIUpstreamEndpoint(c, "/v1/messages")

	originalModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if originalModel == "" {
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	clientStream := gjson.GetBytes(body, "stream").Bool()

	billingModel := resolveOpenAIForwardModel(account, originalModel, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	if upstreamModel != originalModel {
		rewritten, err := sjson.SetBytes(body, "model", upstreamModel)
		if err != nil {
			return nil, fmt.Errorf("rewrite model: %w", err)
		}
		body = rewritten
	}
	if normalized, changed := NormalizeGLM53AnthropicThinking(body, upstreamModel); changed {
		body = normalized
	}

	// 记录客户端请求的推理强度：优先 Claude 协议的 output_config.effort；
	// 缺失且 thinking 已启用时，按国产 passback-required 模型兜底为 high
	// （对齐 Anthropic 网关 gateway_handler 的记录语义，避免该路径长期落 NULL）。
	requestedReasoningEffort := NormalizeClaudeOutputEffort(gjson.GetBytes(body, "output_config.effort").String())
	reasoningEffort := ApplyThinkingEnabledFallback(
		requestedReasoningEffort,
		body,
		billingModel,
	)

	// 与 Anthropic 平台 passthrough 相同的 pre-filter：剥离空文本块与上游
	// 无法接受的 web-search 历史块（GLM/Kimi/DeepSeek 对 server_tool_use 400）。
	body = StripEmptyTextBlocks(body)
	body = FilterWebSearchHistoryBlocks(body, upstreamModel)

	// thinking 模式下 DeepSeek 要求工具续接历史回传 thinking 块；缺失时上游
	// 自动恢复失败会返回 400。仅对 DeepSeek 官方账号、thinking 启用时生效。
	if shouldRepairDeepSeekAnthropicThinking(account, body) {
		body = ensureDeepSeekAnthropicThinkingBlocks(body)
	}

	logger.LegacyPrintf("service.gateway", "[CN Anthropic 直通] account=%d(%s) platform=%s model=%s upstream=%s stream=%v",
		account.ID, account.Name, account.Platform, originalModel, upstreamModel, clientStream)

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

	upstreamCtx, releaseUpstreamCtx := detachStreamUpstreamContext(ctx, clientStream)
	upstreamReq, forwardedBody, err := s.buildNativeAnthropicUpstreamRequest(upstreamCtx, c, account, body, apiKey, targetURL)
	releaseUpstreamCtx()
	if err != nil {
		return nil, err
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
		if foErr := s.failoverOpenAIUpstreamHTTPError(ctx, c, account, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		// 非 failover 错误：经共享 compat handler 以 Anthropic 格式回写
		// （透传规则、ops 记录、cyber_policy 与 CC 回退路径一致）。
		return s.handleAnthropicErrorResponse(resp, c, account, billingModel)
	}

	if clientStream {
		return s.handleNativeAnthropicStreamingResponse(ctx, resp, c, account, originalModel, billingModel, upstreamModel, reasoningEffort, startTime)
	}
	return s.handleNativeAnthropicBufferedResponse(ctx, resp, c, account, originalModel, billingModel, upstreamModel, reasoningEffort, startTime)
}

// nativeAnthropicTargetURL 组装国产供应商原生 Anthropic messages 端点。
// 第三方端点保持朴素路径，不附加 ?beta=true。
func (s *OpenAIGatewayService) nativeAnthropicTargetURL(account *Account) (string, error) {
	baseURL := strings.TrimSpace(account.GetAnthropicProtocolBaseURL())
	if baseURL == "" {
		return "", fmt.Errorf("account %d has no anthropic protocol base url", account.ID)
	}
	validatedURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return nativeAnthropicMessagesURL(account, validatedURL), nil
}

// nativeAnthropicMessagesURL 由已校验的 Anthropic 协议基址拼出 messages 端点，转发与
// 连接测试共用。按模型分流的聚合平台（OpenCode、Command Code 等）的基址可能沿用带 /v1
// 的 Chat Completions 基址，用版本感知拼接避免 /v1/v1/messages；其余供应商朴素拼接。
func nativeAnthropicMessagesURL(account *Account, validatedBaseURL string) string {
	if account.routesByModel() {
		return buildOpenAIEndpointURL(validatedBaseURL, "/v1/messages")
	}
	return strings.TrimRight(validatedBaseURL, "/") + "/v1/messages"
}

func resolveMappedUpstreamModel(account *Account, body []byte, defaultMappedModel string) string {
	original := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	billing := resolveOpenAIForwardModel(account, original, defaultMappedModel)
	return normalizeOpenAIModelForUpstream(account, billing)
}

func (s *OpenAIGatewayService) buildNativeAnthropicUpstreamRequest(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	apiKey string,
	targetURL string,
	sessionBodies ...[]byte,
) (*http.Request, []byte, error) {
	// 能力维度 body sanitize：与 Anthropic 平台 passthrough 相同，按 beta
	// header 决定是否保留 body 中的 beta 能力字段，避免客户端"body 带字段但
	// header 忘带 token"的 bug 让第三方上游 400。
	clientBeta := ""
	if c != nil && c.Request != nil {
		clientBeta = getHeaderRaw(c.Request.Header, "anthropic-beta")
	}
	if beta, ok := account.HeaderOverrideValue("anthropic-beta"); ok {
		clientBeta = beta
	}
	if sanitized, changed := sanitizeAnthropicBodyForBetaTokens(body, clientBeta); changed {
		body = sanitized
	}

	// Ollama Cloud DeepSeek 出站 max_tokens clamp：判定与 nativeAnthropicTargetURL
	// 的 base 取值同源（GetAnthropicProtocolBaseURL，adaptive 时是 Anthropic 协议
	// 地址而非 CC/Responses 地址），详见 helper 注释。
	body = clampOllamaCloudAnthropicMessagesMaxTokens(account, account.GetAnthropicProtocolBaseURL(), body)

	req, err := http.NewRequestWithContext(withOAuth429WireBody(ctx, body), http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}

	if c != nil && c.Request != nil {
		for key, values := range c.Request.Header {
			lowerKey := strings.ToLower(strings.TrimSpace(key))
			if !allowedHeaders[lowerKey] {
				continue
			}
			wireKey := resolveWireCasing(key)
			for _, v := range values {
				addHeaderRaw(req.Header, wireKey, v)
			}
		}
	}

	// 覆盖入站鉴权残留，注入上游认证（默认 x-api-key；可经 extra
	// anthropic_apikey_auth_scheme 切换 Authorization: Bearer；Ollama Cloud
	// 上游按实际 base_url 强制 Bearer，与 nativeAnthropicTargetURL 同源）。
	req.Header.Del("authorization")
	req.Header.Del("x-api-key")
	req.Header.Del("x-goog-api-key")
	req.Header.Del("cookie")
	setAnthropicAPIKeyAuthHeader(req.Header, account, apiKey, account.GetAnthropicProtocolBaseURL())

	if getHeaderRaw(req.Header, "content-type") == "" {
		setHeaderRaw(req.Header, "content-type", "application/json")
	}
	if getHeaderRaw(req.Header, "anthropic-version") == "" {
		setHeaderRaw(req.Header, "anthropic-version", "2023-06-01")
	}

	// 官方 OpenCode / Command Code 上游收敛为规范客户端 UA：客户端透传的编程库
	// UA 会命中其前置 Cloudflare bot 拦截（CF 1010/403），并被计入账号 403 strike。
	applyOpenCodeUpstreamUserAgent(account, targetURL, req.Header)

	// 账号级请求头覆写（最终生效，覆盖上面所有来源的同名头）
	account.ApplyHeaderOverrides(req.Header)
	filterSonnet55ToolsetBetaHeader(req.Header, body, gjson.GetBytes(body, "model").String())
	payloads := append([][]byte{body}, sessionBodies...)
	applyOpenCodeSessionHeader(c, account, targetURL, req.Header, payloads...)

	return req, body, nil
}

// handleNativeAnthropicBufferedResponse 处理非流式原生 Anthropic 响应：
// 校验 JSON、解析 usage、透传响应头后原样回写（仅工具名反向还原）。
func (s *OpenAIGatewayService) handleNativeAnthropicBufferedResponse(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	if s.rateLimitService != nil {
		s.rateLimitService.UpdateSessionWindow(ctx, account, resp.Header)
	}

	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, anthropicTooLargeError)
	if err != nil {
		if resetErr, handled := handleOpenAIHTTP2PeerReset(c, account, resp.Header.Get("x-request-id"), err); handled {
			return nil, resetErr
		}
		return nil, err
	}
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	observer.ObserveAnthropic(body)

	var raw json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, invalidNonStreamingJSONFailoverError(ctx, s.rateLimitService, resp, account, body, err, billingModel)
	}

	usage := parseClaudeUsageFromResponseBody(body)
	if IsForceCacheBilling(ctx) && usage.InputTokens > 0 {
		body, err = classifyAnthropicResponseInputAsCacheRead(body, usage)
		if err != nil {
			return nil, err
		}
	}

	writeAnthropicPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	body = reverseToolNamesIfPresent(c, body)
	c.Data(resp.StatusCode, contentType, body)

	result := &OpenAIForwardResult{
		RequestID:                   resp.Header.Get("x-request-id"),
		UpstreamHeaders:             resp.Header,
		Usage:                       claudeUsageToOpenAIUsage(usage),
		UsageUnavailable:            !anthropicUsageFieldsObserved(gjson.GetBytes(body, "usage")),
		Model:                       originalModel,
		BillingModel:                billingModel,
		UpstreamModel:               upstreamModel,
		UpstreamResponseServiceTier: observedUpstreamResponseServiceTier(c),
		UpstreamEndpoint:            "/v1/messages",
		Stream:                      false,
		ReasoningEffort:             reasoningEffort,
		Duration:                    time.Since(startTime),
	}
	applyObservedUpstreamResponseModel(c, result)
	return result, nil
}

// handleNativeAnthropicStreamingResponse 处理流式原生 Anthropic 响应：
// 字节级 SSE 中继（逐行透传、按事件边界 flush），同时解析 usage。
// 骨架与 handleStreamingResponseAnthropicAPIKeyPassthrough 一致。
func (s *OpenAIGatewayService) handleNativeAnthropicStreamingResponse(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	result, err := s.relayAnthropicStream(ctx, resp, c, account, originalModel, upstreamModel, startTime, true)
	if result == nil {
		return nil, err
	}
	return s.nativeAnthropicStreamResult(c, resp, result.usage, result.usageUnavailable, result.firstTokenMs, result.clientDisconnect, originalModel, billingModel, upstreamModel, reasoningEffort, startTime), err
}

// relayAnthropicStream shares framing, integrity, bounded drain and terminal
// handling between native Messages and Anthropic API-key passthrough.
func (s *OpenAIGatewayService) relayAnthropicStream(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, originalModel, upstreamModel string, startTime time.Time, handlePeerReset bool) (*streamingResult, error) {
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	if s.rateLimitService != nil {
		s.rateLimitService.UpdateSessionWindow(ctx, account, resp.Header)
	}

	writeAnthropicPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "text/event-stream"
	}
	c.Header("Content-Type", contentType)
	if c.Writer.Header().Get("Cache-Control") == "" {
		c.Header("Cache-Control", "no-cache")
	}
	if c.Writer.Header().Get("Connection") == "" {
		c.Header("Connection", "keep-alive")
	}
	c.Header("X-Accel-Buffering", "no")
	if v := resp.Header.Get("x-request-id"); v != "" {
		c.Header("x-request-id", v)
	}

	w := c.Writer

	usage := &ClaudeUsage{}
	var firstTokenMs *int
	clientDisconnected := false
	var pendingLines []string
	var terminalFrames strings.Builder
	status := &anthropicCompatStream{body: resp.Body, pump: &anthropicNativeLinePump{}}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Split(anthropicSSELineSplit())
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	status.maxLineSize = maxLineSize
	defer func() { _ = resp.Body.Close() }()
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)

	type scanEvent struct {
		line string
		err  error
	}
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	sendEvent := func(ev scanEvent) bool {
		select {
		case events <- ev:
			return true
		case <-done:
			return false
		}
	}
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func(scanBuf *sseScannerBuf64K) {
		defer putSSEScannerBuf64K(scanBuf)
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}(scanBuf)
	defer close(done)

	streamInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	keepaliveInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamKeepaliveInterval > 0 {
		keepaliveInterval = time.Duration(s.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}
	var keepaliveTimer *time.Timer
	if keepaliveInterval > 0 {
		keepaliveTimer = time.NewTimer(keepaliveInterval)
		defer keepaliveTimer.Stop()
	}
	var keepaliveCh <-chan time.Time
	if keepaliveTimer != nil {
		keepaliveCh = keepaliveTimer.C
	}
	lastDataAt := time.Now()
	resetKeepaliveTimer := func() {
		if keepaliveTimer == nil {
			return
		}
		if !keepaliveTimer.Stop() {
			select {
			case <-keepaliveTimer.C:
			default:
			}
		}
		keepaliveTimer.Reset(keepaliveInterval)
	}
	inPartialEvent := false
	clientCtx := ctx
	if c.Request != nil {
		clientCtx = c.Request.Context()
	}
	clientGone := clientCtx.Done()
	upstreamGone := ctx.Done()
	markDisconnected := func() {
		clientDisconnected = true
		clientGone = nil
		upstreamGone = nil
		if intervalTicker == nil {
			streamInterval = anthropicCompatDisconnectDrainInterval
			intervalTicker = time.NewTicker(streamInterval)
			intervalCh = intervalTicker.C
		}
	}
	defer func() {
		if intervalTicker != nil {
			intervalTicker.Stop()
		}
	}()

	result := func() *streamingResult {
		return &streamingResult{usageUnavailable: !status.usageSeen, usage: usage, firstTokenMs: firstTokenMs, clientDisconnect: clientDisconnected}
	}
	fail := func() (*streamingResult, error) {
		status.clientDisconnected = clientDisconnected
		failure := status.failure()
		if failure == nil {
			return result(), nil
		}
		if !clientDisconnected {
			if status.upstreamErrPayload != "" {
				return result(), writeNativeAnthropicSemanticFailure(c, []byte(status.upstreamErrPayload), originalModel)
			}
			payload, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": failure.errType, "message": failure.message}})
			MarkOpsStreamError(c, failure.errType, failure.message, http.StatusBadGateway)
			MarkResponseCommitted(c)
			_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
			if err := FlushGatewayResponse(w); err != nil {
				markDisconnected()
			}
		}
		return result(), failure.err
	}
	consume := func(line string) bool {
		name, payload, ready, err := status.frame.line(line, maxLineSize)
		if err != nil {
			status.readErr = err
			return false
		}
		pendingLines = append(pendingLines, line)
		if line != "" {
			return true
		}
		raw := strings.Join(pendingLines, "\n") + "\n"
		pendingLines = nil
		if strings.TrimSpace(payload) == "[DONE]" {
			return true
		}
		if !ready && knownAnthropicStreamEvent(name) {
			return true
		}
		if ready {
			event, known := status.decodeFrame(name, payload)
			if status.protocolFailure != "" {
				return false
			}
			if known {
				observer.ObserveAnthropic([]byte(payload))
				status.observe(&event, payload)
				parseSSEUsagePassthrough(payload, usage)
				if model := compositeClientModel(ctx, ""); model != "" && event.Type == "message_start" && gjson.Get(payload, "message.model").Exists() {
					if rewritten, err := sjson.Set(payload, "message.model", model); err == nil {
						var compact bytes.Buffer
						if json.Compact(&compact, []byte(rewritten)) == nil {
							raw = "event: message_start\ndata: " + compact.String() + "\n\n"
						}
					}
				}
				if firstTokenMs == nil && status.semanticContent {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}
				if event.Type == "error" {
					return false
				}
				if event.Type == "message_stop" || (event.Type == "message_delta" && event.Delta != nil && event.Delta.StopReason != "") {
					if len(raw) > 2*maxLineSize-terminalFrames.Len() {
						status.readErr = errAnthropicSSEFrameTooLarge
						return false
					}
					_, _ = terminalFrames.WriteString(raw)
					return true
				}
			}
		}
		if !clientDisconnected {
			if _, err := io.WriteString(w, reverseToolNamesIfPresentString(c, raw)); err != nil {
				markDisconnected()
			} else {
				if err := FlushGatewayResponse(w); err != nil {
					markDisconnected()
				}
				lastDataAt = time.Now()
				resetKeepaliveTimer()
			}
		}
		return true
	}

	for {
		if !clientDisconnected && (clientCtx.Err() != nil || ctx.Err() != nil) {
			markDisconnected()
		}
		select {
		case ev, ok := <-events:
			if !clientDisconnected && (clientCtx.Err() != nil || ctx.Err() != nil) {
				markDisconnected()
			}
			if !ok {
				if len(pendingLines) > 0 && !consume("") {
					return fail()
				}
				status.readErr = io.EOF
				status.clientDisconnected = clientDisconnected
				if status.failure() != nil {
					return fail()
				}
				if _, err := io.WriteString(w, reverseToolNamesIfPresentString(c, terminalFrames.String())); err != nil {
					markDisconnected()
					return fail()
				}
				if err := FlushGatewayResponse(w); err != nil {
					markDisconnected()
				}
				if clientDisconnected {
					return fail()
				}
				return result(), nil
			}
			if ev.err != nil {
				if errors.Is(ev.err, context.Canceled) || errors.Is(ev.err, context.DeadlineExceeded) {
					markDisconnected()
				}
				status.readErr = ev.err
				if handlePeerReset && !clientDisconnected {
					if resetErr, handled := handleOpenAIHTTP2PeerReset(c, account, resp.Header.Get("x-request-id"), ev.err); handled {
						return result(), resetErr
					}
				}
				return fail()
			}
			inPartialEvent = ev.line != ""
			if !consume(ev.line) {
				return fail()
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if !clientDisconnected && s.rateLimitService != nil {
				s.rateLimitService.HandleStreamTimeout(ctx, account, upstreamModel)
			}
			status.readErr = errAnthropicNativeStreamIdle
			status.pump = &anthropicNativeLinePump{interval: streamInterval}
			return fail()

		case <-clientGone:
			markDisconnected()
		case <-upstreamGone:
			markDisconnected()

		case <-keepaliveCh:
			if clientDisconnected {
				continue
			}
			if inPartialEvent {
				resetKeepaliveTimer()
				continue
			}
			if time.Since(lastDataAt) < keepaliveInterval {
				resetKeepaliveTimer()
				continue
			}
			if _, err := fmt.Fprint(w, "event: ping\ndata: {\"type\": \"ping\"}\n\n"); err != nil {
				markDisconnected()
				logger.LegacyPrintf("service.gateway", "[CN Anthropic 直通] Client disconnected during keepalive ping, continue draining upstream for usage: account=%d", account.ID)
				continue
			}
			if err := FlushGatewayResponse(w); err != nil {
				markDisconnected()
			}
			lastDataAt = time.Now()
			resetKeepaliveTimer()
		}
	}
}

// nativeAnthropicStreamResult 组装流式直通结果；流中断时同样返回已观测到的
// usage 与错误一起带出，避免上游已计量的请求漏记漏计费（对齐 issue #5148 语义）。
func (s *OpenAIGatewayService) nativeAnthropicStreamResult(
	c *gin.Context,
	resp *http.Response,
	usage *ClaudeUsage,
	usageUnavailable bool,
	firstTokenMs *int,
	clientDisconnect bool,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
) *OpenAIForwardResult {
	if usage == nil {
		usage = &ClaudeUsage{}
	}
	result := &OpenAIForwardResult{
		RequestID:                   resp.Header.Get("x-request-id"),
		UpstreamHeaders:             resp.Header,
		Usage:                       claudeUsageToOpenAIUsage(usage),
		UsageUnavailable:            usageUnavailable,
		Model:                       originalModel,
		BillingModel:                billingModel,
		UpstreamModel:               upstreamModel,
		UpstreamResponseServiceTier: observedUpstreamResponseServiceTier(c),
		UpstreamEndpoint:            "/v1/messages",
		Stream:                      true,
		ReasoningEffort:             reasoningEffort,
		Duration:                    time.Since(startTime),
		FirstTokenMs:                firstTokenMs,
		ClientDisconnect:            clientDisconnect,
	}
	applyObservedUpstreamResponseModel(c, result)
	return result
}

// claudeUsageToOpenAIUsage 把 Anthropic 格式 usage 映射到 OpenAI 网关统一的
// 用量结构。Anthropic 的 input_tokens 不含缓存读写，而 OpenAI 网关内部
// 约定 InputTokens 是包含缓存明细的总输入；这里必须先合并，RecordUsage
// 才能准确拆回互斥的计费桶。
func claudeUsageToOpenAIUsage(u *ClaudeUsage) OpenAIUsage {
	if u == nil {
		return OpenAIUsage{}
	}
	return OpenAIUsage{
		InputTokens:                     u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens,
		OutputTokens:                    u.OutputTokens,
		CacheCreationInputTokens:        u.CacheCreationInputTokens,
		CacheCreation5mTokens:           u.CacheCreation5mTokens,
		CacheCreation1hTokens:           u.CacheCreation1hTokens,
		CacheReadInputTokens:            u.CacheReadInputTokens,
		cacheCreationInputTokensPresent: u.cacheCreationInputTokensPresent,
	}
}
