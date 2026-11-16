package service

import (
	"strings"

	"github.com/gin-gonic/gin"
)

const openAIWSRecoveryClientModelKey = "openai_ws_recovery_client_model"

type openAIWSRecoveryClientModel struct{ model string }

// SetOpenAIWSRecoveryClientModel records the current frame's effective client
// model after the handler's fresh auth preflight. The ingress resolves omitted
// models from its last accepted client model before invoking that preflight.
// Keep this separate from channel/account mapping and from diagnostic fields.
func SetOpenAIWSRecoveryClientModel(c *gin.Context, model string) {
	if c != nil {
		c.Set(openAIWSRecoveryClientModelKey, openAIWSRecoveryClientModel{strings.TrimSpace(model)})
	}
}

func openAIWSRecoveryClientModelFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, _ := c.Get(openAIWSRecoveryClientModelKey)
	current, _ := value.(openAIWSRecoveryClientModel)
	return current.model
}
