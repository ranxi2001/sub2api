package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// OpenAIReloginHandler serves credential operations and explicit 2FA imports.
type OpenAIReloginHandler struct {
	svc    *service.OpenAIReloginService
	reauth *service.OpenAIOAuthReauthService
}

func NewOpenAIReloginHandler(svc *service.OpenAIReloginService, reauth *service.OpenAIOAuthReauthService) *OpenAIReloginHandler {
	return &OpenAIReloginHandler{svc: svc, reauth: reauth}
}

func (h *OpenAIReloginHandler) StartTwoFALogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var input struct {
		service.OpenAIReloginCredentials
		CredentialTarget string `json:"credential_target"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "登录凭据格式不正确")
		return
	}
	entry := input.OpenAIReloginCredentials
	if err := service.ValidateOpenAITwoFALogin(entry); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if input.CredentialTarget != "" && input.CredentialTarget != "operations" {
		response.BadRequest(c, "登录凭据仅支持凭证运营")
		return
	}
	status, statusErr := h.reauth.CredentialEncryptionStatus()
	if statusErr != nil {
		response.ErrorFrom(c, statusErr)
		return
	}
	if !status.Configured {
		response.BadRequest(c, "请先在凭证运营中启用凭据加密，再进行 2FA 导入")
		return
	}
	job, err := h.svc.StartTwoFALogin(c.Request.Context(), entry)
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	response.Accepted(c, job)
}

func (h *OpenAIReloginHandler) TwoFALogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	job, ok := h.svc.TwoFALogin(c.Param("id"))
	if !ok {
		response.NotFound(c, "登录任务不存在或已过期")
		return
	}
	response.Success(c, job)
}

func (h *OpenAIReloginHandler) DeleteTwoFALogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	h.svc.DeleteTwoFALogin(c.Param("id"))
	response.Success(c, gin.H{"deleted": true})
}

func (h *OpenAIReloginHandler) Config(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	cfg, err := h.svc.GetConfig(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "无法读取重登服务配置")
		return
	}
	response.Success(c, cfg)
}
func (h *OpenAIReloginHandler) SaveConfig(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var cfg service.OpenAIReloginConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		response.BadRequest(c, "重登服务配置格式不正确")
		return
	}
	saved, err := h.svc.SaveConfig(c.Request.Context(), cfg)
	if err != nil {
		response.BadRequest(c, "无法保存重登服务配置，请检查地址与请求头")
		return
	}
	response.Success(c, saved)
}
