package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *OpenAIOAuthReauthHandler) ListAuthConnections(c *gin.Context) {
	id, ok := parseReauthAccountID(c)
	if !ok {
		return
	}
	result, err := h.service.ListAuthConnections(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *OpenAIOAuthReauthHandler) CreateAuthConnection(c *gin.Context) {
	id, ok := parseReauthAccountID(c)
	if !ok {
		return
	}
	var input service.CreateOpenAIAuthConnectionInput
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Connection kind and label are required")
		return
	}
	result, err := h.service.CreateAuthConnection(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *OpenAIOAuthReauthHandler) UpdateAuthConnection(c *gin.Context) {
	id, ok := parseReauthAccountID(c)
	if !ok {
		return
	}
	var input service.UpdateOpenAIAuthConnectionInput
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Connection revision and action are required")
		return
	}
	result, err := h.service.UpdateAuthConnection(c.Request.Context(), id, c.Param("connection_id"), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *OpenAIOAuthReauthHandler) BeginConnectionAuthorization(c *gin.Context) {
	id, ok := parseReauthAccountID(c)
	if !ok {
		return
	}
	result, err := h.service.BeginConnectionAuthorization(c.Request.Context(), id, c.Param("connection_id"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *OpenAIOAuthReauthHandler) CompleteConnectionAuthorization(c *gin.Context) {
	id, ok := parseReauthAccountID(c)
	if !ok {
		return
	}
	var input struct {
		SessionID   string `json:"session_id" binding:"required"`
		CallbackURL string `json:"callback_url" binding:"required"`
	}
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Session and callback are required")
		return
	}
	err := h.service.CompleteConnectionAuthorization(c.Request.Context(), id, c.Param("connection_id"), input.SessionID, input.CallbackURL)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"authorized": true})
}

// Use a body rather than URL parameters so session identifiers are not copied
// to ordinary access logs, browser history or referrers.
func (h *OpenAIOAuthReauthHandler) ConnectionAuthorizationStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := parseReauthAccountID(c)
	if !ok {
		return
	}
	var input struct {
		SessionID string `json:"session_id" binding:"required"`
	}
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Session is required")
		return
	}
	result, err := h.service.ConnectionAuthorizationStatus(c.Request.Context(), id, c.Param("connection_id"), input.SessionID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *OpenAIOAuthReauthHandler) RevokeAuthConnection(c *gin.Context) {
	id, ok := parseReauthAccountID(c)
	if !ok {
		return
	}
	var input struct {
		Revision int64 `json:"revision" binding:"required"`
	}
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Revision is required")
		return
	}
	confirmed, err := h.service.RevokeAuthConnection(c.Request.Context(), id, c.Param("connection_id"), input.Revision)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"unlinked": true, "remote_revocation_confirmed": confirmed})
}

func (h *OpenAIOAuthReauthHandler) SelectLegacyAuthConnection(c *gin.Context) {
	id, ok := parseReauthAccountID(c)
	if !ok {
		return
	}
	if err := h.service.SelectLegacyAuthConnection(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"legacy": true})
}
func (h *OpenAIOAuthReauthHandler) RotateConnectionAPIKey(c *gin.Context) {
	id, ok := parseReauthAccountID(c)
	if !ok {
		return
	}
	var input struct {
		Revision int64  `json:"revision" binding:"required"`
		APIKey   string `json:"api_key" binding:"required"`
	}
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Revision and API key are required")
		return
	}
	result, err := h.service.RotateConnectionAPIKey(c.Request.Context(), id, c.Param("connection_id"), input.Revision, input.APIKey)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
