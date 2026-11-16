package admin

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func rotationPrivate(c *gin.Context) bool {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	if _, observer := service.ObserverGroupIDs(c.Request.Context()); observer {
		response.Forbidden(c, "Administrator access required")
		return false
	}
	return true
}
func (h *OpenAIOAuthReauthHandler) RotationStatus(c *gin.Context) {
	if !rotationPrivate(c) {
		return
	}
	id, ok := parseReauthAccountID(c)
	if !ok {
		return
	}
	result, err := h.service.GetTOTPRotation(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *OpenAIOAuthReauthHandler) RotateTOTP(c *gin.Context) {
	if !rotationPrivate(c) {
		return
	}
	id, ok := parseReauthAccountID(c)
	if !ok {
		return
	}
	result, err := h.service.CreateTOTPRotation(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Accepted(c, result)
}
func (h *OpenAIOAuthReauthHandler) RetryTOTP(c *gin.Context) {
	if !rotationPrivate(c) {
		return
	}
	id, ok := parseReauthAccountID(c)
	if !ok {
		return
	}
	var req struct {
		TaskID int64  `json:"task_id"`
		Action string `json:"action"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.TaskID <= 0 {
		response.BadRequest(c, "Invalid verification request")
		return
	}
	if err := h.service.RetryTOTPRotation(c.Request.Context(), id, req.TaskID, req.Action); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"accepted": true})
}
func (h *OpenAIOAuthReauthHandler) ExportTOTP(c *gin.Context) {
	if !rotationPrivate(c) {
		return
	}
	id, ok := parseReauthAccountID(c)
	if !ok {
		return
	}
	var req struct {
		Source          string `json:"source"`
		IncludePassword bool   `json:"include_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid export request")
		return
	}
	result, err := h.service.ExportTOTP(c.Request.Context(), id, req.Source, req.IncludePassword)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *OpenAIOAuthReauthHandler) ClaimTOTP(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !h.requireWorker(c) {
		return
	}
	var req reauthWorkerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid worker request")
		return
	}
	result, err := h.service.ClaimTOTPRotation(c.Request.Context(), req.WorkerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *OpenAIOAuthReauthHandler) TOTPPhase(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !h.requireWorker(c) {
		return
	}
	task, err := strconv.ParseInt(c.Param("task_id"), 10, 64)
	if err != nil || task <= 0 {
		response.BadRequest(c, "Invalid task")
		return
	}
	var req struct {
		WorkerID            string `json:"worker_id"`
		Phase               string `json:"phase"`
		Secret              string `json:"secret"`
		FactorID            string `json:"factor_id"`
		EnrollmentSessionID string `json:"enrollment_session_id"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if err = c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid phase request")
		return
	}
	if err = h.service.TOTPRotationPhase(c.Request.Context(), task, req.WorkerID, req.Phase, req.Secret, req.FactorID, req.EnrollmentSessionID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"accepted": true})
}
func (h *OpenAIOAuthReauthHandler) TOTPFinish(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !h.requireWorker(c) {
		return
	}
	task, err := strconv.ParseInt(c.Param("task_id"), 10, 64)
	if err != nil || task <= 0 {
		response.BadRequest(c, "Invalid task")
		return
	}
	var req struct {
		WorkerID  string `json:"worker_id"`
		Success   bool   `json:"success"`
		ErrorCode string `json:"error_code"`
	}
	if err = c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid completion request")
		return
	}
	if err = h.service.FinishTOTPRotation(c.Request.Context(), task, req.WorkerID, req.Success, req.ErrorCode); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"accepted": true})
}

func (h *OpenAIOAuthReauthHandler) TOTPRecover(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !h.requireWorker(c) {
		return
	}
	task, err := strconv.ParseInt(c.Param("task_id"), 10, 64)
	if err != nil || task <= 0 {
		response.BadRequest(c, "Invalid task")
		return
	}
	var req struct {
		Secret              string `json:"secret"`
		FactorID            string `json:"factor_id"`
		EnrollmentSessionID string `json:"enrollment_session_id"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if err = c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid recovery request")
		return
	}
	if err = h.service.RecoverTOTPCandidate(c.Request.Context(), task, req.Secret, req.FactorID, req.EnrollmentSessionID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"accepted": true})
}

// Recovery uses a task ID, since the original account may have been soft-deleted.
func (h *OpenAIOAuthReauthHandler) ExportTOTPRecovery(c *gin.Context) {
	if !rotationPrivate(c) {
		return
	}
	var req struct {
		TaskID          int64  `json:"task_id"`
		Email           string `json:"expected_email"`
		Source          string `json:"source"`
		IncludePassword bool   `json:"include_password"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if err := c.ShouldBindJSON(&req); err != nil || req.TaskID <= 0 {
		response.BadRequest(c, "Invalid recovery request")
		return
	}
	result, err := h.service.ExportTOTPRecovery(c.Request.Context(), req.TaskID, req.Email, req.Source, req.IncludePassword)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *OpenAIOAuthReauthHandler) ImportTOTPRecovery(c *gin.Context) {
	if !rotationPrivate(c) {
		return
	}
	id, ok := parseReauthAccountID(c)
	if !ok {
		return
	}
	var req struct {
		TaskID int64  `json:"task_id"`
		Email  string `json:"expected_email"`
		Source string `json:"source"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if err := c.ShouldBindJSON(&req); err != nil || req.TaskID <= 0 {
		response.BadRequest(c, "Invalid recovery request")
		return
	}
	if err := h.service.ImportTOTPRecovery(c.Request.Context(), id, req.TaskID, req.Email, req.Source); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"imported": true, "verified": false, "engine": "local_worker"})
}
