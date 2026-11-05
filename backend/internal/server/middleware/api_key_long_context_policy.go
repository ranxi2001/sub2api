package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requestmodel"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type APIKeyLongContextChecker func(context.Context, *service.APIKey, []byte, ...string) error

// APIKeyLongContextPolicy runs before a pod route can proxy the request and
// after local model rewriting. Count-only and non-text endpoints are exempt.
func APIKeyLongContextPolicy(check APIKeyLongContextChecker) gin.HandlerFunc {
	if check == nil {
		var gateway *service.GatewayService
		check = gateway.CheckAPIKeyLongContext
	}
	return func(c *gin.Context) {
		service.SetAPIKeyLongContextForwardChecker(c, check)
		key, ok := GetAPIKeyFromContext(c)
		if !ok || key == nil || key.AllowLongContext || c.Request == nil ||
			c.Request.Method != http.MethodPost || !apiKeyLongContextRequest(c) {
			c.Next()
			return
		}
		body, ok := readAdmissionRequestBody(c)
		if !ok {
			return
		}
		models := requestmodel.FromBodyCandidates(c.FullPath(), c.GetHeader("Content-Type"), body)
		if model := groupModelAllowlistModelFromParams(c); model != "" {
			models = append(models, model)
		}
		if err := check(c.Request.Context(), key, body, models...); err != nil {
			rejectAPIKeyLongContext(c, err)
			return
		}
		c.Next()
	}
}

func apiKeyLongContextRequest(c *gin.Context) bool {
	path := c.Request.URL.Path
	if strings.HasSuffix(path, "/messages/count_tokens") || strings.HasSuffix(path, "/responses/input_tokens") {
		return false
	}
	if strings.Contains(path, "/models/") {
		action := strings.TrimPrefix(c.Param("modelAction"), "/")
		return strings.HasSuffix(action, ":generateContent") || strings.HasSuffix(action, ":streamGenerateContent")
	}
	return service.IsAPIKeyModelPolicyPath(path) || strings.HasSuffix(path, "/systemone") || strings.HasSuffix(path, "/cachedContents")
}

func rejectAPIKeyLongContext(c *gin.Context, err error) {
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
	MarkIngressRejected(c, IngressRejectLongContextDisabled)
	code := "context_unverifiable"
	var policyErr *service.APIKeyLongContextError
	if errors.As(err, &policyErr) {
		code = policyErr.Code
	}
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/v1beta/") || strings.HasPrefix(path, "/antigravity/v1beta/") {
		GoogleErrorWriter(c, http.StatusBadRequest, err.Error())
	} else if strings.HasSuffix(path, "/messages") {
		c.JSON(http.StatusBadRequest, gin.H{"type": "error", "error": gin.H{"type": "invalid_request_error", "message": err.Error(), "code": code}})
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "code": code, "message": err.Error()}})
	}
	c.Abort()
}
