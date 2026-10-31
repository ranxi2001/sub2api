package handler

import (
	"context"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type LotteryHandler struct{ lottery *service.LotteryService }

func NewLotteryHandler(lottery *service.LotteryService) *LotteryHandler {
	return &LotteryHandler{lottery: lottery}
}

func lotteryID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid ID")
		return 0, false
	}
	return id, true
}

func (h *LotteryHandler) Pools(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	data, err := h.lottery.Pools(c.Request.Context(), subject.UserID, false)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

func (h *LotteryHandler) Draw(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	id, ok := lotteryID(c, "id")
	if !ok {
		return
	}
	data, err := h.lottery.Draw(c.Request.Context(), subject.UserID, id, c.GetHeader("Idempotency-Key"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

func (h *LotteryHandler) Records(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	page, size := response.ParsePagination(c)
	pool, _ := strconv.ParseInt(c.Query("pool_id"), 10, 64)
	data, total, err := h.lottery.Draws(c.Request.Context(), subject.UserID, pool, page, size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, data, total, page, size)
}

func (h *LotteryHandler) AdminPools(c *gin.Context) {
	data, err := h.lottery.Pools(c.Request.Context(), 0, true)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

func (h *LotteryHandler) Settings(c *gin.Context) {
	data, err := h.lottery.Settings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

func (h *LotteryHandler) SaveSettings(c *gin.Context) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.Enabled == nil {
		response.BadRequest(c, "enabled must be a boolean")
		return
	}
	executeUserIdempotentJSON(c, "admin.lottery.settings.save", input, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.lottery.SetEnabled(ctx, *input.Enabled)
	})
}

func (h *LotteryHandler) DeletePool(c *gin.Context) {
	id, ok := lotteryID(c, "id")
	if !ok {
		return
	}
	executeUserIdempotentJSON(c, "admin.lottery.pool.delete", gin.H{"id": id}, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		err := h.lottery.DeletePool(ctx, id)
		return gin.H{"deleted": true}, err
	})
}

func (h *LotteryHandler) DeletePrize(c *gin.Context) {
	id, ok := lotteryID(c, "id")
	if !ok {
		return
	}
	prizeID, ok := lotteryID(c, "prize_id")
	if !ok {
		return
	}
	executeUserIdempotentJSON(c, "admin.lottery.prize.delete", gin.H{"pool_id": id, "prize_id": prizeID}, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		err := h.lottery.DeletePrize(ctx, id, prizeID)
		return gin.H{"deleted": true}, err
	})
}

func (h *LotteryHandler) SavePool(c *gin.Context) {
	var id int64
	if c.Param("id") != "" {
		var ok bool
		id, ok = lotteryID(c, "id")
		if !ok {
			return
		}
	}
	var input service.LotteryPoolInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid pool")
		return
	}
	executeUserIdempotentJSON(c, "admin.lottery.pool.save", gin.H{"id": id, "body": input}, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.lottery.SavePool(ctx, id, input)
	})
}

func (h *LotteryHandler) SavePrize(c *gin.Context) {
	poolID, ok := lotteryID(c, "id")
	if !ok {
		return
	}
	var prizeID int64
	if c.Param("prize_id") != "" {
		prizeID, ok = lotteryID(c, "prize_id")
		if !ok {
			return
		}
	}
	var input service.LotteryPrizeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid prize")
		return
	}
	executeUserIdempotentJSON(c, "admin.lottery.prize.save", gin.H{"pool_id": poolID, "prize_id": prizeID, "body": input}, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.lottery.SavePrize(ctx, poolID, prizeID, input)
	})
}

func (h *LotteryHandler) AdminRecords(c *gin.Context) {
	page, size := response.ParsePagination(c)
	uid, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	pid, _ := strconv.ParseInt(c.Query("pool_id"), 10, 64)
	data, total, err := h.lottery.Draws(c.Request.Context(), uid, pid, page, size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, data, total, page, size)
}

func (h *LotteryHandler) AdminLedger(c *gin.Context) {
	page, size := response.ParsePagination(c)
	uid, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	pid, _ := strconv.ParseInt(c.Query("pool_id"), 10, 64)
	data, total, err := h.lottery.Ledger(c.Request.Context(), uid, pid, page, size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, data, total, page, size)
}
