package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const apiKeyLongContextForwardCheckerKey = "api_key_long_context_forward_checker"

type apiKeyLongContextForwardChecker func(context.Context, *APIKey, []byte, ...string) error

// SetAPIKeyLongContextForwardChecker shares the ingress pricing service with
// protocol adapters without adding a billing dependency to every adapter.
func SetAPIKeyLongContextForwardChecker(c *gin.Context, check func(context.Context, *APIKey, []byte, ...string) error) {
	if c != nil && check != nil {
		c.Set(apiKeyLongContextForwardCheckerKey, apiKeyLongContextForwardChecker(check))
	}
}

// Recheck the actual body and account model after protocol conversion, mapping,
// and prompt injection. The current key is read again after queue revalidation.
func checkAPIKeyLongContextBeforeForward(ctx context.Context, c *gin.Context, billing *BillingService, body []byte, models ...string) error {
	if !apiKeyLongContextForwardApplies(c) {
		return nil
	}
	key := getAPIKeyFromContext(c)
	if key == nil || key.AllowLongContext {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if marked, _ := ctx.Value(apiKeyLongContextOutboundModelKey{}).(string); marked == "" {
		if model := gjson.GetBytes(body, "model").String(); model != "" {
			ctx = withAPIKeyLongContextOutboundModel(ctx, model)
		} else if len(models) > 0 {
			ctx = withAPIKeyLongContextOutboundModel(ctx, models[0])
		}
	}
	models = append(models, APIKeyQueueRequestPermissionsFromContext(c.Request.Context()).Models...)
	check := apiKeyLongContextForwardChecker(func(ctx context.Context, key *APIKey, body []byte, models ...string) error {
		return checkAPIKeyLongContext(ctx, billing, key, body, models...)
	})
	if value, ok := c.Get(apiKeyLongContextForwardCheckerKey); ok {
		if registered, ok := value.(apiKeyLongContextForwardChecker); ok {
			check = registered
		}
	}
	if err := check(ctx, key, body, models...); err != nil {
		writeAPIKeyLongContextForwardError(c, err)
		return err
	}
	return nil
}

func checkAPIKeyLongContextRequestBeforeForward(ctx context.Context, c *gin.Context, req *http.Request, models ...string) error {
	if !apiKeyLongContextForwardApplies(c) {
		return nil
	}
	key := getAPIKeyFromContext(c)
	if key == nil || key.AllowLongContext {
		return nil
	}
	if req == nil || req.GetBody == nil {
		err := &APIKeyLongContextError{Code: "context_unverifiable", Reason: "outbound context is not replayable"}
		writeAPIKeyLongContextForwardError(c, err)
		return err
	}
	reader, err := req.GetBody()
	if err != nil {
		writeAPIKeyLongContextForwardError(c, err)
		return err
	}
	defer func() { _ = reader.Close() }()
	body, err := io.ReadAll(reader)
	if err != nil {
		writeAPIKeyLongContextForwardError(c, err)
		return err
	}
	if len(models) > 0 {
		// URL-based Gemini requests carry the original model first and the
		// selected account's final model last; neither is stored in the body.
		ctx = withAPIKeyLongContextOutboundModel(ctx, models[len(models)-1])
	}
	return checkAPIKeyLongContextBeforeForward(ctx, c, nil, body, models...)
}

func apiKeyLongContextForwardApplies(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.Method != http.MethodPost {
		return false
	}
	path := c.Request.URL.Path
	if strings.HasSuffix(path, "/messages/count_tokens") || strings.HasSuffix(path, "/responses/input_tokens") {
		return false
	}
	if strings.Contains(path, "/models/") {
		action := strings.TrimPrefix(c.Param("modelAction"), "/")
		return strings.HasSuffix(action, ":generateContent") || strings.HasSuffix(action, ":streamGenerateContent")
	}
	return IsAPIKeyModelPolicyPath(path) || strings.HasSuffix(path, "/systemone") || strings.HasSuffix(path, "/cachedContents")
}

func writeAPIKeyLongContextForwardError(c *gin.Context, err error) {
	MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
	code := "context_unverifiable"
	if policyErr, ok := err.(*APIKeyLongContextError); ok {
		code = policyErr.Code
	}
	path := c.Request.URL.Path
	var payload gin.H
	event := ""
	if strings.HasPrefix(path, "/v1beta/") || strings.HasPrefix(path, "/antigravity/v1beta/") {
		payload = gin.H{"error": gin.H{"code": http.StatusBadRequest, "message": err.Error(), "status": "INVALID_ARGUMENT"}}
	} else if strings.HasSuffix(path, "/messages") {
		event = "event: error\n"
		payload = gin.H{"type": "error", "error": gin.H{"type": "invalid_request_error", "code": code, "message": err.Error()}}
	} else {
		payload = gin.H{"error": gin.H{"type": "invalid_request_error", "code": code, "message": err.Error()}}
	}
	if c.Writer.Written() {
		// Queue keepalives may already have committed HTTP 200. Preserve SSE
		// framing and prevent the handler from appending its generic fallback.
		MarkOpsStreamError(c, "invalid_request_error", err.Error(), http.StatusBadRequest)
		if strings.HasSuffix(path, "/responses") || strings.HasSuffix(path, "/responses/compact") {
			writeOpenAICompactSSEFailureMessage(c, http.StatusBadRequest, code, err.Error())
		} else {
			data, _ := json.Marshal(payload)
			if _, writeErr := io.WriteString(c.Writer, event+"data: "+string(data)+"\n\n"); writeErr != nil {
				_ = c.Error(writeErr)
			}
			if flushErr := FlushGatewayResponse(c.Writer); flushErr != nil {
				_ = c.Error(flushErr)
			}
		}
	} else {
		c.JSON(http.StatusBadRequest, payload)
	}
	MarkResponseCommitted(c)
	c.Abort()
}
