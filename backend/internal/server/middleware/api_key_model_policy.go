package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requestmodel"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// APIKeyModelPolicy runs after the client-model allowlist and before composite
// routing. Both the public alias and mapped model must retain group permission.
// WebSocket frames are handled by the Responses ingress on every turn.
func APIKeyModelPolicy() gin.HandlerFunc {
	return func(c *gin.Context) {
		key, ok := GetAPIKeyFromContext(c)
		if !ok || key == nil || len(key.ModelMapping) == 0 || c.Request.Method != http.MethodPost || !service.IsAPIKeyModelPolicyPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		body, ok := readAdmissionRequestBody(c)
		if !ok {
			return
		}
		if !gjson.ValidBytes(body) {
			c.Next() // Preserve the endpoint's existing malformed-body response.
			return
		}
		clientModels := requestmodel.FromBodyCandidates(c.FullPath(), c.GetHeader("Content-Type"), body)
		models := service.APIKeyModelCandidates(key, clientModels)
		for _, model := range models {
			if key.Group != nil && key.Group.ModelAllowlistEnabled() && !key.Group.ModelAllowlist.Allows(model) {
				rejectAPIKeyMappedModel(c, fmt.Sprintf("Model %q is not available for this group", model))
				return
			}
		}
		if denied := service.FirstUserGroupDeniedModel(key.DeniedModelsInGroup(), models); denied != "" {
			rejectAPIKeyMappedModel(c, fmt.Sprintf("Model %q is not available for your account in this group", denied))
			return
		}
		// Capture both identities for queue revalidation, including permissions
		// enabled while this request waits for a slot.
		permissions := service.APIKeyQueueRequestPermissionsFromContext(c.Request.Context())
		permissions.Models = models
		c.Request = c.Request.WithContext(service.WithAPIKeyQueueRequestPermissions(c.Request.Context(), permissions))
		original := gjson.GetBytes(body, "model").String()
		mapped := service.MapAPIKeyModel(key, original)
		canonicalize := mapped != original
		for _, candidate := range clientModels {
			canonicalize = canonicalize || service.MapAPIKeyModel(key, candidate) != candidate
		}
		if original != "" && canonicalize {
			// Canonicalize duplicate/case-variant model keys so encoding/json
			// adapters cannot restore a different client value after this rewrite.
			var object map[string]json.RawMessage
			err := json.Unmarshal(body, &object)
			if err == nil {
				for field := range object {
					if strings.EqualFold(field, "model") {
						delete(object, field)
					}
				}
				object["model"], err = json.Marshal(mapped)
			}
			updated, marshalErr := json.Marshal(object)
			if err == nil {
				err = marshalErr
			}
			if err != nil {
				AbortWithError(c, http.StatusBadRequest, "invalid_request_error", "Invalid model mapping request")
				return
			}
			if mapped != original {
				ctx := context.WithValue(c.Request.Context(), ctxkey.RequestedPublicModel, original)
				c.Request = c.Request.WithContext(service.WithAPIKeyMappedModel(ctx, mapped))
			}
			requestmodel.ResetRequestBody(c.Request, updated)
		}
		c.Next()
	}
}

func rejectAPIKeyMappedModel(c *gin.Context, message string) {
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalModelConfiguration)
	MarkIngressRejected(c, IngressRejectModelNotAllowed)
	groupModelAllowlistErrorWriter(c)(c, http.StatusNotFound, message)
	c.Abort()
}
