package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requestmodel"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// GeminiInboundUsesNativeUpstream 报告这次 /v1beta/models 请求是否仍走 Gemini
// 或 Antigravity 原生上游。其他平台改走 Chat Completions 桥，由账号自己的协议
// 分流决定上游端点。
func GeminiInboundUsesNativeUpstream(c *gin.Context) bool {
	if middleware.HasForcePlatform(c) {
		return true
	}
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.Group == nil {
		return true
	}
	switch platform := effectiveAPIKeyPlatform(c, apiKey); platform {
	case "", service.PlatformGemini, service.PlatformAntigravity, service.PlatformComposite:
		return true
	default:
		return !service.PlatformAcceptsGeminiInboundBridge(platform)
	}
}

// SelectGeminiChatCompletionsHandler 按分组平台选择已经存在的 Chat Completions
// 入口。OpenAI 网关族会再按账号协议转到 /v1/chat/completions、/v1/responses 或
// /v1/messages；Anthropic 分组转到 /v1/messages。
func SelectGeminiChatCompletionsHandler(platform string, openAI *OpenAIGatewayHandler, gateway *GatewayHandler) gin.HandlerFunc {
	if !service.PlatformAcceptsGeminiInboundBridge(platform) {
		return nil
	}
	if domain.UsesOpenAIGateway(platform) {
		if openAI == nil {
			return nil
		}
		return openAI.ChatCompletions
	}
	if gateway == nil {
		return nil
	}
	return gateway.ChatCompletions
}

// ServeGeminiViaChatCompletions 把 /v1beta/models/{model}:{action} 转成 Chat
// Completions 请求，复用对应网关的调度、计费和上游协议分流，再把响应写回 Gemini 格式。
func ServeGeminiViaChatCompletions(c *gin.Context, chat gin.HandlerFunc) {
	if c == nil {
		return
	}
	if chat == nil {
		googleError(c, http.StatusBadRequest, "API key group platform is not supported for /v1beta/models")
		return
	}
	modelName, action, err := parseGeminiModelAction(strings.TrimPrefix(c.Param("modelAction"), "/"))
	if err != nil {
		googleError(c, http.StatusNotFound, err.Error())
		return
	}
	if !acceptableBridgedModelName(modelName) {
		googleError(c, http.StatusBadRequest, "Invalid model in URL")
		return
	}
	// 复合分组把公开模型解析成上游模型后只写在上下文里。Chat Completions 的 model
	// 必须用这个上游模型，URL 里的名字只作为没解析到时的回落。
	if resolvedModel, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context()); ok {
		if resolved := strings.TrimSpace(resolvedModel); acceptableBridgedModelName(resolved) {
			modelName = resolved
		}
	}
	switch action {
	case gemini.ActionGenerateContent, gemini.ActionStreamGenerateContent, gemini.ActionCountTokens:
	default:
		googleError(c, http.StatusNotFound, "Unsupported action: "+action)
		return
	}
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if middleware.RejectRequestBodyReadError(c, err) {
			return
		}
		if maxErr, ok := extractMaxBytesError(err); ok {
			googleError(c, http.StatusRequestEntityTooLarge, buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		googleError(c, http.StatusBadRequest, "Failed to read request body")
		return
	}
	if len(body) == 0 {
		googleError(c, http.StatusBadRequest, "Request body is empty")
		return
	}
	if action == gemini.ActionCountTokens {
		if !gjson.ValidBytes(body) {
			googleError(c, http.StatusBadRequest, "Failed to parse request body")
			return
		}
		tokens, characters := apicompat.EstimateGeminiTotalTokens(body)
		c.JSON(http.StatusOK, gin.H{"totalTokens": tokens, "totalBillableCharacters": characters})
		return
	}

	stream := action == gemini.ActionStreamGenerateContent
	chatBody, err := apicompat.GeminiGenerateContentToChatCompletions(modelName, body, stream)
	if err != nil {
		googleError(c, http.StatusBadRequest, err.Error())
		return
	}
	requestmodel.ResetRequestBody(c.Request, chatBody)
	writer := newGeminiChatResponseWriter(c.Writer, modelName)
	c.Writer = writer
	defer writer.finish()
	chat(c)
}

func (h *GatewayHandler) writeBridgedGeminiModelList(c *gin.Context, apiKey *service.APIKey) {
	ids := h.bridgedGeminiModelIDs(c, apiKey)
	models := make([]gemini.Model, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		models = append(models, gemini.FallbackModel(id))
	}
	c.JSON(http.StatusOK, gemini.ModelsListResponse{Models: models})
}

func (h *GatewayHandler) writeBridgedGeminiModel(c *gin.Context, modelName string) {
	if !acceptableBridgedModelName(modelName) {
		googleError(c, http.StatusBadRequest, "Invalid model in URL")
		return
	}
	c.JSON(http.StatusOK, gemini.FallbackModel(modelName))
}

func (h *GatewayHandler) bridgedGeminiModelIDs(c *gin.Context, apiKey *service.APIKey) []string {
	platform := effectiveAPIKeyPlatform(c, apiKey)
	var ids []string
	if h != nil && h.gatewayService != nil && apiKey != nil {
		ids = h.gatewayService.GeminiBridgeModelIDs(c.Request.Context(), apiKey.GroupID, platform)
	}
	if ids == nil {
		ids = service.DefaultModelIDsForPlatform(platform)
	}
	if apiKey != nil && apiKey.Group != nil && apiKey.Group.ModelAllowlistEnabled() {
		ids = apiKey.Group.ModelAllowlist.FilterForListing(ids)
	}
	if apiKey != nil {
		ids = service.FilterUserGroupDeniedModelIDs(ids, apiKey.DeniedModelsInGroup())
	}
	return ids
}

func acceptableBridgedModelName(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" || len(model) > 256 || strings.Contains(model, "..") {
		return false
	}
	return !strings.ContainsAny(model, " \t\r\n?#\\")
}

type geminiChatResponseWriter struct {
	gin.ResponseWriter
	model      string
	status     int
	buf        bytes.Buffer
	sawWrite   bool
	streaming  bool
	committed  bool
	finished   bool
	translator *apicompat.GeminiSSETranslator
}

func newGeminiChatResponseWriter(base gin.ResponseWriter, model string) *geminiChatResponseWriter {
	return &geminiChatResponseWriter{
		ResponseWriter: base,
		model:          model,
		translator:     apicompat.NewGeminiSSETranslator(model),
	}
}

func (w *geminiChatResponseWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *geminiChatResponseWriter) Size() int {
	if w.streaming {
		return w.ResponseWriter.Size()
	}
	if w.buf.Len() > 0 {
		return w.buf.Len()
	}
	return w.ResponseWriter.Size()
}

func (w *geminiChatResponseWriter) Written() bool {
	return w.sawWrite || w.committed || w.ResponseWriter.Written()
}

func (w *geminiChatResponseWriter) WriteHeader(code int) {
	if code > 0 {
		w.status = code
	}
}

func (w *geminiChatResponseWriter) WriteHeaderNow() {
	if w.streaming {
		w.commitStreamHeader()
	}
}

func (w *geminiChatResponseWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *geminiChatResponseWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if !w.streaming && strings.Contains(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream") {
		w.streaming = true
	}
	if w.streaming {
		w.commitStreamHeader()
		converted := w.translator.Push(p)
		if len(converted) == 0 {
			w.sawWrite = true
			return len(p), nil
		}
		_, err := w.ResponseWriter.Write(converted)
		w.sawWrite = true
		if err != nil {
			return 0, err
		}
		if bytes.HasPrefix(converted, []byte(":")) || bytes.Contains(converted, []byte("\n:")) {
			w.ResponseWriter.Flush()
		}
		return len(p), nil
	}
	_, _ = w.buf.Write(p)
	w.sawWrite = true
	return len(p), nil
}

func (w *geminiChatResponseWriter) Flush() {
	if w.streaming && w.committed {
		w.ResponseWriter.Flush()
	}
}

func (w *geminiChatResponseWriter) commitStreamHeader() {
	if w.committed {
		return
	}
	w.committed = true
	w.Header().Del("Content-Length")
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	w.ResponseWriter.WriteHeader(w.Status())
}

func (w *geminiChatResponseWriter) finish() {
	if w == nil || w.finished {
		return
	}
	w.finished = true
	if w.streaming {
		if extra := w.translator.Flush(); len(extra) > 0 {
			w.commitStreamHeader()
			_, _ = w.ResponseWriter.Write(extra)
		}
		if w.committed {
			w.ResponseWriter.Flush()
		}
		return
	}
	if !w.sawWrite {
		if w.status != 0 {
			w.ResponseWriter.WriteHeader(w.status)
		}
		return
	}
	status := w.Status()
	body := w.buf.Bytes()
	header := w.Header()
	header.Del("Content-Length")
	out, status := geminiBodyFromChatResponse(status, body, w.model)
	header.Set("Content-Type", "application/json; charset=utf-8")
	w.ResponseWriter.WriteHeader(status)
	_, _ = w.ResponseWriter.Write(out)
}

func geminiBodyFromChatResponse(status int, body []byte, model string) ([]byte, int) {
	if status >= 400 || gjson.GetBytes(body, "error").Exists() && !gjson.GetBytes(body, "choices").Exists() {
		if status < 400 {
			status = http.StatusBadGateway
		}
		return googleErrorBody(status, openAIErrorMessage(body)), status
	}
	converted, err := apicompat.ChatCompletionsJSONToGemini(body, model)
	if err != nil {
		return googleErrorBody(http.StatusBadGateway, "Failed to convert upstream response"), http.StatusBadGateway
	}
	return converted, status
}

func openAIErrorMessage(body []byte) string {
	message := strings.TrimSpace(gjson.GetBytes(body, "error.message").String())
	if message == "" {
		message = strings.TrimSpace(gjson.GetBytes(body, "message").String())
	}
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" {
		return "Upstream request failed"
	}
	return message
}

func googleErrorBody(status int, message string) []byte {
	if status < 400 {
		status = http.StatusBadGateway
	}
	encoded, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    status,
			"message": message,
			"status":  googleapi.HTTPStatusToGoogleStatus(status),
		},
	})
	if err != nil {
		return []byte(`{"error":{"code":502,"message":"Upstream request failed","status":"INTERNAL"}}`)
	}
	return encoded
}
