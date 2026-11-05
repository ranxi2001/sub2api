package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var aiOpsDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (h *OpsHandler) aiOpsControl(c *gin.Context, requireEnabled bool) (service.AIOpsRepository, bool) {
	if !allowAIOps(c) {
		return nil, false
	}
	if h.opsService == nil || (requireEnabled && !h.opsService.AIOpsControlEnabled()) {
		response.ErrorWithDetails(c, http.StatusServiceUnavailable, "AI operations controls are not enabled", "AI_OPS_CONTROL_DISABLED", nil)
		return nil, false
	}
	repo, err := h.opsService.AIOpsRepository()
	if err != nil {
		aiOpsControlError(c, err)
		return nil, false
	}
	return repo, true
}

func aiOpsControlError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAIOpsConflict):
		response.ErrorWithDetails(c, http.StatusConflict, "The policy, proposal or account changed; refresh before continuing", "AI_OPS_CONFLICT", nil)
	case errors.Is(err, service.ErrAIOpsBlocked):
		response.ErrorWithDetails(c, http.StatusConflict, "AI operations safety conditions do not permit this action", "AI_OPS_BLOCKED", nil)
	default:
		response.ErrorWithDetails(c, http.StatusServiceUnavailable, "AI operations controls are unavailable", "AI_OPS_UNAVAILABLE", nil)
	}
}

func aiOpsDecode(c *gin.Context, out any, maxBytes int64) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		response.BadRequest(c, "Invalid AI operations request")
		return false
	}
	return true
}

func aiOpsActor(c *gin.Context) int64 {
	actor, _ := middleware.GetAuthSubjectFromContext(c)
	return actor.UserID
}

func (h *OpsHandler) GetAIOpsPolicy(c *gin.Context) {
	repo, ok := h.aiOpsControl(c, false)
	if !ok {
		return
	}
	policy, err := repo.AIOpsPolicy(c.Request.Context())
	if err != nil {
		aiOpsControlError(c, err)
		return
	}
	response.Success(c, struct {
		service.AIOpsPolicy
		ControlEnabled bool `json:"control_enabled"`
	}{policy, h.opsService.AIOpsControlEnabled()})
}

func (h *OpsHandler) SaveAIOpsPolicy(c *gin.Context) {
	repo, ok := h.aiOpsControl(c, true)
	if !ok {
		return
	}
	var input struct {
		service.AIOpsPolicy
		ControlEnabled bool `json:"control_enabled"`
	}
	if !aiOpsDecode(c, &input, 32768) {
		return
	}
	if err := service.ValidateAIOpsPolicy(input.AIOpsPolicy); err != nil {
		response.BadRequest(c, "Invalid AI operations policy or execution budget")
		return
	}
	policy, err := repo.SaveAIOpsPolicy(c.Request.Context(), input.AIOpsPolicy, input.Revision, aiOpsActor(c))
	if err != nil {
		aiOpsControlError(c, err)
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"config_version": policy.Revision, "enabled": policy.Mode != "disabled"})
	response.Success(c, struct {
		service.AIOpsPolicy
		ControlEnabled bool `json:"control_enabled"`
	}{policy, true})
}

func (h *OpsHandler) HaltAIOps(c *gin.Context) {
	repo, ok := h.aiOpsControl(c, false)
	if !ok {
		return
	}
	var input struct{}
	if !aiOpsDecode(c, &input, 1024) {
		return
	}
	policy, err := repo.HaltAIOps(c.Request.Context(), aiOpsActor(c))
	if err != nil {
		aiOpsControlError(c, err)
		return
	}
	response.Success(c, struct {
		service.AIOpsPolicy
		ControlEnabled bool `json:"control_enabled"`
	}{policy, h.opsService.AIOpsControlEnabled()})
}

func (h *OpsHandler) PreviewAIOpsProposal(c *gin.Context) {
	repo, ok := h.aiOpsControl(c, true)
	if !ok {
		return
	}
	var input struct {
		RunID         string `json:"run_id"`
		ProposalIndex int    `json:"proposal_index"`
	}
	if !aiOpsDecode(c, &input, 1024) {
		return
	}
	id, err := uuid.Parse(input.RunID)
	if err != nil || id.String() != input.RunID || input.ProposalIndex < 0 || input.ProposalIndex >= 8 {
		response.BadRequest(c, "Invalid diagnostic run or proposal index")
		return
	}
	// Fetch the immutable candidate ourselves; callers cannot supply a target,
	// value, group, evidence, timestamp or 'approved' flag through this route.
	var run aiOpsRun
	status, err := h.aiOps.request(c.Request.Context(), http.MethodGet, "/v1/runs/"+input.RunID, &run)
	if err != nil || run.ID != input.RunID || !validAIOpsRun(&run) || run.Status != "completed" || run.Result == nil || input.ProposalIndex >= len(run.Result.Proposals) {
		aiOpsProxyError(c, aiOpsErrorStatus(status, err))
		return
	}
	candidate := run.Result.Proposals[input.ProposalIndex]
	observedAt, err := time.Parse(time.RFC3339Nano, run.Window.End)
	if err != nil || candidate.AccountID == nil || candidate.Action == "none" {
		response.BadRequest(c, "This diagnostic proposal cannot be prepared for an account action")
		return
	}
	policy, err := repo.AIOpsPolicy(c.Request.Context())
	if err != nil || policy.GroupID != run.GroupID {
		aiOpsControlError(c, service.ErrAIOpsBlocked)
		return
	}
	for _, evidence := range run.Evidence {
		if evidence.Status != "ok" {
			aiOpsControlError(c, service.ErrAIOpsBlocked)
			return
		}
	}
	proposal, err := repo.PreviewAIOps(c.Request.Context(), service.AIOpsPreviewInput{
		RunID: run.ID, ProposalIndex: input.ProposalIndex, Action: candidate.Action,
		AccountID: *candidate.AccountID, ProposedValue: candidate.ProposedValue, EvidenceObservedAt: observedAt,
	}, aiOpsActor(c))
	if err != nil {
		aiOpsControlError(c, err)
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"source": "codex_sdk", "event_id": proposal.ID})
	response.Success(c, proposal)
}

func (h *OpsHandler) ApplyAIOpsProposal(c *gin.Context) {
	repo, ok := h.aiOpsControl(c, true)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id.String() != c.Param("id") {
		response.BadRequest(c, "Invalid proposal ID")
		return
	}
	var input struct {
		Digest  string `json:"digest"`
		Confirm bool   `json:"confirm"`
	}
	if !aiOpsDecode(c, &input, 1024) {
		return
	}
	if !input.Confirm || !aiOpsDigestPattern.MatchString(input.Digest) {
		response.BadRequest(c, "Confirm the exact frozen proposal digest")
		return
	}
	action, err := repo.ApplyAIOps(c.Request.Context(), id.String(), input.Digest, aiOpsActor(c), false)
	if err != nil {
		aiOpsControlError(c, err)
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"source": "human_approved_ai_ops", "event_id": action.ID})
	response.Success(c, action)
}

func (h *OpsHandler) ListAIOpsActions(c *gin.Context) {
	repo, ok := h.aiOpsControl(c, false)
	if !ok {
		return
	}
	actions, err := repo.ListAIOpsActions(c.Request.Context())
	if err != nil {
		aiOpsControlError(c, err)
		return
	}
	if actions == nil {
		actions = []service.AIOpsAction{}
	}
	response.Success(c, gin.H{"items": actions, "total": len(actions)})
}

func (h *OpsHandler) RollbackAIOpsAction(c *gin.Context) {
	repo, ok := h.aiOpsControl(c, true)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id.String() != c.Param("id") {
		response.BadRequest(c, "Invalid action ID")
		return
	}
	var input struct {
		ExpectedRevision int64 `json:"expected_revision"`
		Confirm          bool  `json:"confirm"`
	}
	if !aiOpsDecode(c, &input, 1024) {
		return
	}
	if !input.Confirm || input.ExpectedRevision < 1 {
		response.BadRequest(c, "Explicit rollback confirmation and action ownership revision are required")
		return
	}
	action, err := repo.RollbackAIOps(c.Request.Context(), id.String(), input.ExpectedRevision, aiOpsActor(c))
	if err != nil {
		aiOpsControlError(c, err)
		return
	}
	response.Success(c, action)
}

func (h *OpsHandler) ListAIOpsProbes(c *gin.Context) {
	repo, ok := h.aiOpsControl(c, false)
	if !ok {
		return
	}
	items, err := repo.ListAIOpsProbes(c.Request.Context())
	if err != nil {
		aiOpsControlError(c, err)
		return
	}
	if items == nil {
		items = []service.AIOpsProbe{}
	}
	response.Success(c, gin.H{"items": items, "total": len(items)})
}

func (h *OpsHandler) CreateAIOpsProbe(c *gin.Context) {
	_, ok := h.aiOpsControl(c, true)
	if !ok {
		return
	}
	var input struct {
		AccountID int64  `json:"account_id"`
		Model     string `json:"model"`
		Confirm   bool   `json:"confirm"`
	}
	if !aiOpsDecode(c, &input, 1024) {
		return
	}
	if !input.Confirm || input.AccountID <= 0 || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,99}$`).MatchString(input.Model) {
		response.BadRequest(c, "Explicitly authorize one account and a known text model recovery probe")
		return
	}
	probe, err := h.opsService.StartAIOpsProbe(c.Request.Context(), input.AccountID, input.Model, aiOpsActor(c))
	if err != nil {
		aiOpsControlError(c, err)
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"source": "authorized_recovery_probe", "event_id": probe.ID})
	response.Accepted(c, probe)
}
