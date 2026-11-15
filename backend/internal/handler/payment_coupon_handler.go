package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *PaymentHandler) ListMyCoupons(c *gin.Context) {
	subject, ok := requireAuth(c)
	if !ok {
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.paymentService.ListCoupons(c.Request.Context(), subject.UserID, 0, c.Query("status"), "", page, size)
	if response.ErrorFrom(c, err) {
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Paginated(c, items, int64(total), page, size)
}
func (h *PaymentHandler) QuoteCoupon(c *gin.Context) {
	subject, ok := requireAuth(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid coupon ID")
		return
	}
	var req struct {
		Amount      float64 `json:"amount"`
		PaymentType string  `json:"payment_type"`
	}
	if err = c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid coupon quote")
		return
	}
	out, err := h.paymentService.QuoteCoupon(c.Request.Context(), subject.UserID, id, req.Amount, req.PaymentType)
	if response.ErrorFrom(c, err) {
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}

func (h *PaymentHandler) ListMyCouponUsage(c *gin.Context) {
	subject, ok := requireAuth(c)
	if !ok {
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.paymentService.ListCouponUsage(c.Request.Context(), subject.UserID, c.Query("search"), c.Query("status"), page, size)
	if response.ErrorFrom(c, err) {
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Paginated(c, items, int64(total), page, size)
}
