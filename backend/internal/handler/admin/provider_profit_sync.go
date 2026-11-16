package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
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
	// Check BEFORE opening a session: even a login on New API is not part of
	// this read. Pinning the revision closes the read-to-login race.
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
	var charges []service.ProviderProfitRemoteCharge
	err = h.withMatureProviderSession(ctx, id, p.Revision, func(ctx context.Context, s *providerSession) error {
		var readErr error
		charges, readErr = readProviderProfitUsage(ctx, s, window)
		return readErr
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrMatureProviderConflict), errors.Is(err, service.ErrMatureProviderNotFound):
			matureProviderError(c, err)
		case errors.Is(err, service.ErrProviderProfitSyncLimit):
			response.BadRequest(c, "Too many usage rows; sync a smaller time window")
		case errors.Is(err, errProviderProfitSimpleMode):
			response.BadRequest(c, "Simple-mode usage is not a provider charge")
		default:
			// Remote error bodies can echo keys, bearer tokens or user data.
			response.Error(c, http.StatusBadGateway, "Could not verify a complete provider usage response; no charges saved")
		}
		return
	}
	out, err := store.ReconcileProviderProfitCharges(ctx, id, epoch, p.BaseURL, window, charges)
	if err != nil {
		if errors.Is(err, service.ErrProviderProfitSyncLimit) {
			response.BadRequest(c, "Too many usage rows; sync a smaller time window")
		} else {
			matureProviderError(c, err)
		}
		return
	}
	response.Success(c, out)
}
