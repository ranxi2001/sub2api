package handler

import (
	"context"
	"encoding/json"
	"errors"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// GrokRealtime exposes xAI's native Voice Realtime WebSocket.
// Only Grok-platform API keys may use this endpoint.
func (h *OpenAIGatewayHandler) GrokRealtime(c *gin.Context) {
	model := ""
	if c != nil && c.Request != nil {
		model = strings.TrimSpace(c.Query("model"))
	}
	if model == "" {
		model = "grok-voice-latest"
	}
	h.serveGrokVoiceWebSocket(c, grokVoiceWebSocket{
		name:     "realtime",
		apiName:  "Realtime API",
		inflight: service.InflightEstimateRequest{Model: model, Kind: service.InflightEstimateAudio, AudioMode: "realtime", AudioUnits: 1},
		open: func(ctx context.Context, account *service.Account, token string) (*service.GrokRealtimeUpstream, error) {
			return h.gatewayService.OpenGrokRealtime(ctx, account, token, model)
		},
		proxy: func(ctx context.Context, conn *coderws.Conn, upstream *service.GrokRealtimeUpstream) (*service.OpenAIForwardResult, error) {
			started := time.Now()
			audioObserved, err := h.gatewayService.ProxyGrokRealtimeConn(ctx, c, conn, upstream)
			return grokRealtimeBillingResult(model, time.Since(started), audioObserved), err
		},
	})
}

// GrokTTSStream exposes xAI's streaming TTS WebSocket (GET /v1/tts with
// Upgrade: websocket). Billing follows HTTP TTS: characters sent in text.delta.
func (h *OpenAIGatewayHandler) GrokTTSStream(c *gin.Context) {
	var query url.Values
	if c != nil && c.Request != nil {
		query = c.Request.URL.Query()
	}
	h.serveGrokVoiceWebSocket(c, grokVoiceWebSocket{
		name:    "tts_stream",
		apiName: "TTS streaming API",
		// Characters are unknown until the client streams them; settle on the actual count.
		inflight: service.InflightEstimateRequest{Model: "tts", Kind: service.InflightEstimateAudio, AudioMode: "tts"},
		open: func(ctx context.Context, account *service.Account, token string) (*service.GrokRealtimeUpstream, error) {
			return h.gatewayService.OpenGrokTTSStream(ctx, account, token, query)
		},
		proxy: func(ctx context.Context, conn *coderws.Conn, upstream *service.GrokRealtimeUpstream) (*service.OpenAIForwardResult, error) {
			started := time.Now()
			chars, err := h.gatewayService.ProxyGrokTTSStreamConn(ctx, conn, upstream)
			return grokTTSStreamBillingResult(chars, time.Since(started)), err
		},
	})
}

// grokVoiceWebSocket describes one xAI Voice WebSocket endpoint relayed by
// serveGrokVoiceWebSocket.
type grokVoiceWebSocket struct {
	name     string
	apiName  string // used in client-facing errors
	inflight service.InflightEstimateRequest
	open     func(ctx context.Context, account *service.Account, token string) (*service.GrokRealtimeUpstream, error)
	// proxy relays the accepted session and returns its billing result (nil = nothing to bill).
	proxy func(ctx context.Context, conn *coderws.Conn, upstream *service.GrokRealtimeUpstream) (*service.OpenAIForwardResult, error)
}

// serveGrokVoiceWebSocket selects a Grok account, completes the upstream
// handshake before accepting the client upgrade (so upstream failures stay
// ordinary HTTP errors with failover), relays the session and bills it.
func (h *OpenAIGatewayHandler) serveGrokVoiceWebSocket(c *gin.Context, ws grokVoiceWebSocket) {
	if c == nil || c.Request == nil || !isOpenAIWSUpgradeRequest(c.Request) {
		h.errorResponse(c, http.StatusUpgradeRequired, "invalid_request_error", "WebSocket upgrade required (Upgrade: websocket)")
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformGrok {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", ws.apiName+" is not supported for this platform")
		return
	}
	if !h.ensureResponsesDependencies(c, nil) {
		return
	}
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}

	reqLog := requestLogger(c, "handler.openai_gateway.grok_"+ws.name)
	// 余额模式在途预留（与计费同口径估算；计费任务扣减余额缓存后才释放）。
	inflightDone, inflightErr := reserveInflightBalance(c, h.billingCacheService, h.gatewayService, apiKey, subscription, ws.inflight)
	if inflightErr != nil {
		status, code, message, retryAfter := billingErrorDetails(inflightErr)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}
	defer inflightDone()

	// Keep the HTTP response uncommitted while selecting and probing an account.
	keyRelease, keyErr := h.concurrencyHelper.AcquireAPIKeySlot(c.Request.Context(), apiKey.ID, apiKey.ConcurrencyLimit)
	if keyErr != nil {
		h.handleConcurrencyError(c, keyErr, "API key", false)
		return
	}
	defer keyRelease()

	// A WebSocket session is not an HTTP streaming response; using reqStream=true
	// here would let the wait queue flush an SSE ping before the handshake succeeds.
	failed := map[int64]struct{}{}
	var forbiddenBudget grokForbiddenFailoverBudget
	upstreamSwitchCount := 0
	var selection *service.AccountSelectionResult
	var release func()
	var token string
	var upstream *service.GrokRealtimeUpstream
	var candidateSeen bool
	for attempts := 0; attempts < 4; attempts++ {
		// Voice models are not a text-model capability. Passing a concrete text
		// model here would reject accounts mapped only to an older/default text
		// model before the upstream handshake can decide. An empty requested
		// model keeps account selection capability-based; the voice model (if
		// any) stays in the upstream WS query.
		candidate, _, selectErr := h.gatewayService.SelectAccountWithSchedulerForCapability(
			c.Request.Context(), apiKey.GroupID, "", "", "", failed,
			service.OpenAIUpstreamTransportHTTPSSE,
			service.OpenAIEndpointCapabilityChatCompletions,
			false, false, false, service.PlatformGrok,
		)
		if selectErr != nil || candidate == nil || candidate.Account == nil {
			break
		}
		candidateSeen = true
		account := candidate.Account
		var streamStarted bool
		var slotStatus openAISlotAcquireResult
		release, slotStatus = h.acquireResponsesAccountSlot(c, apiKey.GroupID, "", candidate, false, &streamStarted, reqLog)
		if slotStatus != openAISlotAcquireOK {
			if slotStatus == openAISlotAcquireFailed {
				return
			}
			failed[account.ID] = struct{}{}
			continue
		}
		var credErr error
		token, _, credErr = h.gatewayService.GetRequestCredential(c.Request.Context(), c, account)
		if credErr != nil {
			release()
			release = nil
			if forbiddenBudget.active {
				break
			}
			failed[account.ID] = struct{}{}
			continue
		}
		probeCtx, cancelProbe := context.WithTimeout(c.Request.Context(), service.DefaultGrokRealtimeDialTimeout)
		candidateUpstream, openErr := ws.open(probeCtx, account, token)
		cancelProbe()
		if openErr != nil {
			reqLog.Warn("grok_"+ws.name+".pre_accept_failed", zap.Int64("account_id", account.ID), zap.Error(openErr))
			statusCode := http.StatusBadGateway
			var responseBody []byte
			var responseHeaders http.Header
			var dialErr *service.GrokRealtimeDialError
			if errors.As(openErr, &dialErr) && dialErr.StatusCode > 0 {
				statusCode = dialErr.StatusCode
				responseBody = dialErr.ResponseBody
				responseHeaders = dialErr.ResponseHeaders
			}
			h.gatewayService.HandleGrokRealtimeUpstreamError(c.Request.Context(), account, statusCode, responseBody)
			release()
			release = nil
			failoverErr := service.GrokRealtimeFailoverError(account, statusCode, responseHeaders, responseBody)
			if !forbiddenBudget.canRetry(failoverErr, upstreamSwitchCount) {
				break
			}
			upstreamSwitchCount++
			failed[account.ID] = struct{}{}
			continue
		}
		selection, upstream = candidate, candidateUpstream
		break
	}
	if selection == nil || selection.Account == nil || release == nil || upstream == nil {
		if !candidateSeen {
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "No available Grok accounts")
		} else {
			h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Grok "+strings.ReplaceAll(ws.name, "_", " ")+" upstream unavailable")
		}
		return
	}
	defer release()
	defer func() { _ = upstream.Close() }()

	conn, err := coderws.Accept(c.Writer, c.Request, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()

	result, proxyErr := ws.proxy(c.Request.Context(), conn, upstream)
	if proxyErr != nil {
		reqLog.Info("grok_"+ws.name+".proxy_failed", zap.Error(proxyErr))
		if !isExpectedGrokRealtimeClose(proxyErr) {
			_ = conn.Close(coderws.StatusInternalError, "upstream voice websocket failed")
			return
		}
	}
	if result != nil {
		h.recordGrokVoiceUsage(c, apiKey, selection.Account, subscription, ws.name, nil, result)
	}
}

// grokTTSStreamBillingResult bills a streaming TTS session like HTTP TTS
// (million characters); a session that sent no text bills nothing.
func grokTTSStreamBillingResult(chars int64, elapsed time.Duration) *service.OpenAIForwardResult {
	if chars <= 0 {
		return nil
	}
	return &service.OpenAIForwardResult{
		RequestID:     service.StableGrokAudioBillingRequestID(""),
		Model:         "tts",
		UpstreamModel: "tts",
		Duration:      elapsed,
		AudioUsage:    &service.AudioUsage{Mode: "tts", DurationOrUnits: float64(chars) / 1_000_000.0},
	}
}

func grokRealtimeBillingResult(model string, elapsed time.Duration, audioObserved bool) *service.OpenAIForwardResult {
	if !audioObserved || elapsed <= 0 {
		return nil
	}
	return &service.OpenAIForwardResult{
		RequestID:  service.StableGrokRealtimeBillingRequestID(""),
		Model:      model,
		Duration:   elapsed,
		AudioUsage: &service.AudioUsage{Mode: "realtime", DurationOrUnits: elapsed.Minutes()},
	}
}

func isExpectedGrokRealtimeClose(err error) bool {
	if err == nil {
		return true
	}
	switch coderws.CloseStatus(err) {
	case coderws.StatusNormalClosure, coderws.StatusGoingAway,
		coderws.StatusNoStatusRcvd, coderws.StatusAbnormalClosure:
		return true
	default:
		return false
	}
}

// GrokVoice handles xAI Voice HTTP endpoints. endpoint is "tts", "stt", or "custom-voices".
func (h *OpenAIGatewayHandler) GrokVoice(c *gin.Context, endpoint string) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformGrok {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Voice API is not supported for this platform")
		return
	}
	if !h.ensureResponsesDependencies(c, nil) {
		return
	}
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}

	body, err := readGrokVoiceGatewayBody(c)
	if err != nil {
		if middleware2.RejectRequestBodyReadError(c, err) {
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if endpoint == "tts" {
		subject, _ := middleware2.GetAuthSubjectFromContext(c)
		reqLog := requestLogger(c, "handler.openai_gateway.grok_voice", zap.String("endpoint", endpoint))
		// TTS bodies use {"input":"..."} (and variants). Normalize to chat messages so
		// content moderation extractors see the spoken text.
		auditBody := body
		if input := extractGrokTTSInputText(body); input != "" {
			if b, err := json.Marshal(map[string]any{
				"messages": []map[string]any{{"role": "user", "content": input}},
			}); err == nil {
				auditBody = b
			}
		}
		if decision := h.checkSecurityAudit(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIChat, "grok-4.5", auditBody); decision != nil && !decision.AllowNextStage {
			h.openAISecurityAuditError(c, decision)
			return
		}
	}
	// 余额模式在途预留（与计费同口径估算；计费任务扣减余额缓存后才释放）。
	inflightDone, inflightErr := reserveInflightBalance(c, h.billingCacheService, h.gatewayService, apiKey, subscription, grokVoiceInflightEstimate(endpoint, body))
	if inflightErr != nil {
		status, code, message, retryAfter := billingErrorDetails(inflightErr)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}
	defer inflightDone()

	contentType := c.GetHeader("Content-Type")
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/json"
	}

	failed := map[int64]struct{}{}
	var forbiddenBudget grokForbiddenFailoverBudget
	upstreamSwitchCount := 0
	var last *service.UpstreamFailoverError
	reqLog := requestLogger(c, "handler.openai_gateway.grok_voice", zap.String("endpoint", endpoint))
	selectionModel := "grok-4.5"
	keyRelease, keyErr := h.concurrencyHelper.AcquireAPIKeySlot(c.Request.Context(), apiKey.ID, apiKey.ConcurrencyLimit)
	if keyErr != nil {
		h.handleConcurrencyError(c, keyErr, "API key", false)
		return
	}
	defer keyRelease()

	for attempts := 0; attempts < 4; attempts++ {
		selection, _, selectErr := h.gatewayService.SelectAccountWithSchedulerForCapability(
			c.Request.Context(),
			apiKey.GroupID,
			"",
			"",
			selectionModel,
			failed,
			service.OpenAIUpstreamTransportHTTPSSE,
			service.OpenAIEndpointCapabilityChatCompletions,
			false,
			false,
			false,
			service.PlatformGrok,
		)
		if selectErr != nil || selection == nil || selection.Account == nil {
			if last != nil {
				h.handleFailoverExhausted(c, last, false)
			} else {
				h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "No available Grok accounts")
			}
			return
		}
		account := selection.Account
		var started bool
		release, status := h.acquireResponsesAccountSlot(c, apiKey.GroupID, "", selection, false, &started, reqLog)
		if status == openAISlotAcquireProfitVetoed {
			failed[account.ID] = struct{}{}
			continue
		}
		if status != openAISlotAcquireOK {
			// Failed already wrote error response (or transient reject).
			if status == openAISlotAcquireFailed && len(failed) == 0 {
				// Slot path wrote the response; stop.
				return
			}
			failed[account.ID] = struct{}{}
			continue
		}
		result, forwardErr := func() (*service.OpenAIForwardResult, error) {
			defer release()
			return h.gatewayService.ForwardGrokVoice(c.Request.Context(), c, account, endpoint, body, contentType)
		}()
		if forwardErr == nil {
			h.recordGrokVoiceUsage(c, apiKey, account, subscription, endpoint, body, result)
			return
		}
		var failoverErr *service.UpstreamFailoverError
		if errors.As(forwardErr, &failoverErr) {
			if c.Writer.Written() || !forbiddenBudget.canRetry(failoverErr, upstreamSwitchCount) {
				h.handleFailoverExhausted(c, failoverErr, c.Writer.Written())
				return
			}
			upstreamSwitchCount++
			failed[account.ID] = struct{}{}
			last = failoverErr
			continue
		}
		// Non-failover errors: handleGrokMediaErrorResponse / transport already wrote response.
		return
	}
	if last != nil {
		h.handleFailoverExhausted(c, last, false)
	}
}

// recordGrokVoiceUsage bills TTS/STT/realtime via group audio prices when AudioUsage is set.
func (h *OpenAIGatewayHandler) recordGrokVoiceUsage(
	c *gin.Context,
	apiKey *service.APIKey,
	account *service.Account,
	subscription *service.UserSubscription,
	endpoint string,
	body []byte,
	result *service.OpenAIForwardResult,
) {
	if h == nil || c == nil || apiKey == nil || account == nil || result == nil {
		return
	}
	if result.AudioUsage == nil {
		return
	}
	// Ensure forced durable request ids even if callers forget (realtime/tts/stt money path).
	if mode := strings.TrimSpace(result.AudioUsage.Mode); mode == "realtime" {
		result.RequestID = service.StableGrokRealtimeBillingRequestID(result.RequestID)
	} else {
		result.RequestID = service.StableGrokAudioBillingRequestID(result.RequestID)
	}
	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	sessionID := service.ExtractClientSessionID(c)
	requestPayloadHash := service.HashUsageRequestPayload(body)
	if requestPayloadHash == "" {
		requestPayloadHash = service.HashUsageRequestPayload([]byte(endpoint))
	}
	inboundEndpoint := GetInboundEndpoint(c)
	upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
	quotaPlatform := service.QuotaPlatform(c.Request.Context(), apiKey)
	model := strings.TrimSpace(result.Model)
	if model == "" {
		model = endpoint
	}

	h.submitMandatoryUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
		if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result:             result,
			APIKey:             apiKey,
			User:               apiKey.User,
			Account:            account,
			Subscription:       subscription,
			InboundEndpoint:    inboundEndpoint,
			UpstreamEndpoint:   upstreamEndpoint,
			UserAgent:          userAgent,
			IPAddress:          clientIP,
			RequestPayloadHash: requestPayloadHash,
			APIKeyService:      h.apiKeyService,
			QuotaPlatform:      quotaPlatform,
			SessionID:          sessionID,
			ChannelUsageFields: clientRequestedUsageFields(c, service.ChannelMappingResult{}, model, result.UpstreamModel),
		}); err != nil {
			logger.L().With(
				zap.String("component", "handler.openai_gateway.grok_voice"),
				zap.Int64("user_id", apiKey.User.ID),
				zap.Int64("api_key_id", apiKey.ID),
				zap.Any("group_id", apiKey.GroupID),
				zap.String("endpoint", endpoint),
				zap.Int64("account_id", account.ID),
			).Error("grok_voice.record_usage_failed", zap.Error(err))
		}
	})
}

func readGrokVoiceGatewayBody(c *gin.Context) ([]byte, error) {
	if c == nil || c.Request == nil {
		return nil, errors.New("request body is required")
	}
	if c.Request.Body == nil {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodDelete {
			return nil, nil
		}
		return nil, errors.New("request body is required")
	}
	return pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
}

// extractGrokTTSInputText pulls the primary spoken text from a TTS JSON body.
func extractGrokTTSInputText(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	for _, key := range []string{"input", "text", "prompt"} {
		if v, ok := payload[key]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}
