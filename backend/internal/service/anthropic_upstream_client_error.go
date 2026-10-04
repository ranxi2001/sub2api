package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// anthropicUpstreamClientErrorFallbackType 是上游未给出 error.type 时的兜底值。
// 与既有 400 分支保持一致：明确告知这是请求本身的问题，而不是链路故障。
const anthropicUpstreamClientErrorFallbackType = "invalid_request_error"

// anthropicUpstreamClientErrorFallbackMessage 是上游连 message 都没给时的兜底文案。
const anthropicUpstreamClientErrorFallbackMessage = "Upstream rejected the request"

// isAnthropicClientRequestStatus 判断上游状态码是否表示「请求本身非法」的确定性客户端错误。
//
// 这类错误换账号、换 sticky 会话、重试多少次都会得到相同结果，必须原样透传状态码与
// Anthropic 错误体；归一成 502 upstream_error 会让下游把请求错误误判成网关故障并重放。
//
// 排除项：
//   - 401/402/403 是网关运营方的凭据/账单/权限问题，仍包成 502，不向客户端暴露上游账号状态；
//   - 429 已有独立的限流分支（模型级限流/账号冷却）。
func isAnthropicClientRequestStatus(status int) bool {
	switch status {
	case http.StatusUnauthorized,
		http.StatusPaymentRequired,
		http.StatusForbidden,
		http.StatusTooManyRequests:
		return false
	}
	return status >= 400 && status < 500
}

// writeAnthropicUpstreamClientError 以 Anthropic 错误体形状回写确定性客户端错误，
// 保留上游状态码、error.type 与调用方已脱敏的 message。
//
// upstreamMsg 由调用方传入（已 sanitizeUpstreamErrorMessage），这里不回落读取原始 body，
// 避免绕开脱敏。
func writeAnthropicUpstreamClientError(c *gin.Context, statusCode int, body []byte, upstreamMsg string) {
	errType := strings.TrimSpace(gjson.GetBytes(body, "error.type").String())
	if errType == "" {
		errType = anthropicUpstreamClientErrorFallbackType
	}
	message := strings.TrimSpace(upstreamMsg)
	if message == "" {
		message = anthropicUpstreamClientErrorFallbackMessage
	}
	c.JSON(statusCode, gin.H{
		"type":  "error",
		"error": gin.H{"type": errType, "message": message},
	})
}
