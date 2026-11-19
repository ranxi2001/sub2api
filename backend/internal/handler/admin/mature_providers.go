package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func matureProviderError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrMatureProviderValidation) || errors.Is(err, service.ErrMatureProviderConflict) || errors.Is(err, service.ErrMatureProviderNotFound) || errors.Is(err, service.ErrMatureProviderKeyPending) {
		response.ErrorFrom(c, err)
		return
	}
	response.InternalError(c, "Provider catalog unavailable")
}

func (h *ChannelHandler) ListMatureProviders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	page, e1 := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, e2 := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	search, status := strings.TrimSpace(c.Query("search")), c.Query("status")
	if e1 != nil || e2 != nil || page < 1 || page > 1000000 || size < 1 || size > 100 || len(search) > 120 || (status != "" && status != "unknown" && status != "connected" && status != "disconnected") {
		matureProviderError(c, service.ErrMatureProviderValidation)
		return
	}
	r, err := h.channelService.MatureProviderRepository()
	if err != nil {
		matureProviderError(c, err)
		return
	}
	out, err := r.ListMatureProviders(c.Request.Context(), search, status, page, size)
	if err != nil {
		matureProviderError(c, err)
		return
	}
	response.Success(c, out)
}

func (h *ChannelHandler) GetMatureProvider(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		matureProviderError(c, service.ErrMatureProviderValidation)
		return
	}
	r, err := h.channelService.MatureProviderRepository()
	if err != nil {
		matureProviderError(c, err)
		return
	}
	out, err := r.GetMatureProvider(c.Request.Context(), id)
	if err != nil {
		matureProviderError(c, err)
		return
	}
	response.Success(c, out)
}

func (h *ChannelHandler) MutateMatureProvider(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var in service.MatureProviderInput
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
		matureProviderError(c, service.ErrMatureProviderValidation)
		return
	}
	id := int64(0)
	if c.Request.Method != "POST" {
		var err error
		id, err = strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || id <= 0 || in.Revision == nil || *in.Revision < 1 {
			matureProviderError(c, service.ErrMatureProviderValidation)
			return
		}
	}
	if c.Request.Method == "DELETE" {
		if !in.Confirm {
			matureProviderError(c, service.ErrMatureProviderValidation)
			return
		}
	} else if err := service.ValidateMatureProviderInput(&in); err != nil {
		matureProviderError(c, err)
		return
	}
	if c.Request.Method != "DELETE" {
		if err := h.encryptMatureProviderInput(&in); err != nil {
			matureProviderError(c, err)
			return
		}
	}
	r, err := h.channelService.MatureProviderRepository()
	if err != nil {
		matureProviderError(c, err)
		return
	}
	middleware.SetAuditAction(c, "admin.provider."+strings.ToLower(c.Request.Method))
	middleware.SetAuditExtra(c, map[string]any{"provider_id": id, "revision": in.Revision})
	var out *service.MatureProvider
	switch c.Request.Method {
	case "POST":
		out, err = r.CreateMatureProvider(c.Request.Context(), in)
	case "PUT":
		out, err = r.UpdateMatureProvider(c.Request.Context(), id, in)
	case "DELETE":
		err = r.DeleteMatureProvider(c.Request.Context(), id, *in.Revision)
	}
	if err != nil {
		matureProviderError(c, err)
		return
	}
	if out == nil {
		h.profitReports.clear()
		response.Success(c, gin.H{"deleted": id})
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"provider_id": out.ID, "revision": out.Revision})
	h.profitReports.clear()
	response.Success(c, out)
}
