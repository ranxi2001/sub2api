package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *ChannelHandler) SyncProviderProfitCharges(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	var window service.ProviderProfitWindow
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	dec.DisallowUnknownFields()
	if err != nil || id <= 0 || dec.Decode(&window) != nil || dec.Decode(new(any)) != io.EOF || !window.Valid() {
		response.BadRequest(c, "Expected a positive provider ID and RFC3339 from and until no more than 32 days apart")
		return
	}
	ctx := c.Request.Context()
	r, err := h.channelService.MatureProviderRepository()
	if err != nil {
		matureProviderError(c, err)
		return
	}
	store, ok := r.(service.ProviderProfitChargeRepository)
	if !ok {
		response.InternalError(c, "Provider reconciliation unavailable")
		return
	}
	p, err := r.GetMatureProvider(ctx, id)
	if err != nil {
		matureProviderError(c, err)
		return
	}
	if p.SiteType != "sub2api" || p.Revision < 1 {
		response.BadRequest(c, "Usage reconciliation requires a Sub2API provider")
		return
	}
	epoch, err := store.ProviderProfitIdentityFrom(ctx, id)
	if err != nil {
		matureProviderError(c, err)
		return
	}
	middleware.SetAuditAction(c, "admin.provider.sync_profit_charges")
	middleware.SetAuditExtra(c, map[string]any{"provider_id": id})
	background := c.Query("background") == "true"
	// Both types of caller can join the same job. Its lifetime must not
	// depend on which browser started it or keep request credentials alive.
	job, err := h.profitJobs.start(id, window, func(jobCtx context.Context) profitJobStatus {
		return h.runProfitSync(jobCtx, *p, epoch, window, store)
	})
	if err != nil {
		response.Error(c, http.StatusTooManyRequests, "A bill sync is already running; retry later")
		return
	}
	if background {
		response.Success(c, gin.H{"job_id": job.id})
		return
	}
	// Preserve the original API contract for clients not asking for a job.
	select {
	case <-ctx.Done():
		return
	case <-job.done:
	}
	status := job.status // published before closing done; final status is immutable
	switch status.Reason {
	case "":
		response.Success(c, status.ProviderProfitSyncResult)
	case "simple_mode":
		response.BadRequest(c, "Simple-mode usage is not a provider charge")
	case "conflict":
		matureProviderError(c, service.ErrMatureProviderConflict)
	case "upstream":
		response.Error(c, http.StatusBadGateway, "Could not verify a complete provider usage response; no charges saved")
	case "evidence_budget":
		response.BadRequest(c, "Bill sync exceeded the working memory budget; try fewer UTC dates")
	case "rate_limited":
		response.Error(c, http.StatusTooManyRequests, "The provider asked to slow down and the remaining sync time was not enough to finish reading bills; no charges saved")
	case "timeout":
		response.Error(c, http.StatusGatewayTimeout, "Provider reconciliation timed out")
	default:
		response.InternalError(c, "Provider reconciliation unavailable")
	}
}

func (h *ChannelHandler) runProfitSync(ctx context.Context, p service.MatureProvider, epoch time.Time, w service.ProviderProfitWindow, store service.ProviderProfitChargeRepository) profitJobStatus {
	r, err := h.channelService.MatureProviderRepository()
	if err != nil {
		return profitJobStatus{Status: "failed", Reason: "internal"}
	}
	var passwordLogin providerToken // ephemeral and private to this scan
	withSession := func(read func(context.Context, *providerSession) error) error {
		// Refresh can legitimately increment revision between batches. Validate
		// the original identity, then pin this batch to the exact row we read.
		for attempt := 0; ; attempt++ {
			latest, err := r.GetMatureProvider(ctx, p.ID)
			if err != nil {
				return err
			}
			if latest.BaseURL != p.BaseURL || latest.SiteType != p.SiteType || latest.Account != p.Account || latest.AuthMode != p.AuthMode || latest.UserID != p.UserID {
				return service.ErrMatureProviderConflict
			}
			current, err := store.ProviderProfitIdentityFrom(ctx, p.ID)
			if err != nil {
				return err
			}
			if !current.Equal(epoch) {
				return service.ErrMatureProviderConflict
			}
			entered := false
			err = h.withProviderBatchSession(ctx, p.ID, latest.Revision, &passwordLogin, func(batch context.Context, s *providerSession) error {
				entered = true
				return read(batch, s)
			})
			// Login can be rate-limited before this batch reads a page. Retrying
			// that sign-in does not replay rows already observed.
			if !entered && profitIsRateLimited(err) {
				if waitErr := profitBackoffWait(ctx, p.ID, "session", attempt+1, err); waitErr != nil {
					return waitErr
				}
				continue
			}
			// A scheduled dashboard refresh can win the revision check while we
			// wait for its row lock. Revalidate once, but never replay a partial scan.
			if entered || attempt != 0 || !errors.Is(err, service.ErrMatureProviderConflict) {
				return err
			}
		}
	}
	bills, err := readProviderProfitUsageBatches(ctx, p.ID, w, withSession)
	reason := "upstream"
	if err == nil {
		var result service.ProviderProfitSyncResult
		result, err = store.ReconcileProviderProfitCharges(ctx, p.ID, epoch, p.BaseURL, w, bills)
		if err == nil {
			return profitJobStatus{Status: "succeeded", ProviderProfitSyncResult: result}
		}
		reason = "internal"
	}
	switch {
	case errors.Is(err, errProfitRateLimited):
		reason = "rate_limited"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		reason = "timeout"
	case errors.Is(err, errProviderProfitSimpleMode):
		reason = "simple_mode"
	case errors.Is(err, errProviderProfitEvidenceBudget):
		reason = "evidence_budget"
	case errors.Is(err, service.ErrMatureProviderConflict), errors.Is(err, service.ErrMatureProviderNotFound):
		reason = "conflict"
	}
	// Reason only. The upstream error text can echo a response body.
	slog.Warn("provider profit sync failed", "provider_id", p.ID, "reason", reason)
	return profitJobStatus{Status: "failed", Reason: reason}
}

func (h *ChannelHandler) ProviderProfitSyncStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	status, ok := h.profitJobs.get(id, c.Param("job"))
	if err != nil || !ok {
		response.Error(c, http.StatusNotFound, "Bill sync not found or expired")
		return
	}
	response.Success(c, status)
}
