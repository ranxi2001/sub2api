package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// Both routes are registered only within the authenticated admin route group.
func (h *OpenAIReloginHandler) CredentialEncryption(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	status, err := h.reauth.CredentialEncryptionStatus()
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

func (h *OpenAIReloginHandler) InitializeCredentialEncryption(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	status, err := h.reauth.InitializeCredentialEncryption()
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}
