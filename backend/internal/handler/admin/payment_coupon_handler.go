package admin

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *PaymentHandler) ListCouponTemplates(c *gin.Context) {
	items, err := h.paymentService.ListCouponTemplates(c.Request.Context())
	if response.ErrorFrom(c, err) {
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, items)
}
func (h *PaymentHandler) SaveCouponTemplate(c *gin.Context) {
	var id int64
	var ok bool
	if c.Param("id") != "" {
		id, ok = parseIDParam(c, "id")
		if !ok {
			return
		}
	}
	var req service.CouponTemplate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid coupon template")
		return
	}
	out, err := h.paymentService.SaveCouponTemplate(c.Request.Context(), id, req)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, out)
}
func (h *PaymentHandler) IssueCoupons(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var req service.CouponIssueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid coupon issuance")
		return
	}
	ids, err := h.paymentService.IssueCoupons(c.Request.Context(), req)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, gin.H{"ids": ids, "count": len(ids)})
}
func (h *PaymentHandler) ListCoupons(c *gin.Context) {
	page, size := response.ParsePagination(c)
	var uid, tid int64
	var err error
	if raw := c.Query("user_id"); raw != "" {
		uid, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || uid < 1 {
			response.BadRequest(c, "Invalid user ID")
			return
		}
	}
	if raw := c.Query("template_id"); raw != "" {
		tid, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || tid < 1 {
			response.BadRequest(c, "Invalid template ID")
			return
		}
	}
	items, total, err := h.paymentService.ListCoupons(c.Request.Context(), uid, tid, c.Query("status"), c.Query("search"), page, size)
	if response.ErrorFrom(c, err) {
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Paginated(c, items, int64(total), page, size)
}
func (h *PaymentHandler) RevokeCoupon(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if response.ErrorFrom(c, h.paymentService.RevokeCoupon(c.Request.Context(), id)) {
		return
	}
	response.Success(c, gin.H{"success": true})
}

func (h *PaymentHandler) ListCouponUsage(c *gin.Context) {
	page, size := response.ParsePagination(c)
	items, total, err := h.paymentService.ListCouponUsage(c.Request.Context(), 0, c.Query("search"), c.Query("status"), page, size)
	if response.ErrorFrom(c, err) {
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Paginated(c, items, int64(total), page, size)
}
