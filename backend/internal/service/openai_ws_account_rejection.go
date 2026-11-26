package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// An HTTP 401 alone is not evidence about the selected account: proxies and
// capacity relays also return it. Only structured credential evidence may open
// a new cross-account retry window on a stateful transport.
func isOpenAIExplicitAccountCredential401(status int, headers http.Header, body []byte) bool {
	if status != http.StatusUnauthorized || headers.Get("Proxy-Authenticate") != "" ||
		!openAIEncryptedJSONUnambiguous(body) {
		return false
	}
	fields := gjson.GetBytes(body, "error")
	if !fields.IsObject() {
		return false
	}
	code := strings.TrimSpace(fields.Get("code").String())
	// An unknown explicit code blocks the broader error.type fallback. Do not
	// let an embedded status or echoed message manufacture credential evidence.
	errorFields := gin.H{"code": code}
	if code == "" {
		errorFields["type"] = fields.Get("type").String()
	}
	signal, _ := marshalOpenAIUpstreamJSON(gin.H{"error": errorFields})
	return openAIStreamCredentialAuthFailure(signal) || isOpenAIUpstreamAccessStateError("", signal)
}
