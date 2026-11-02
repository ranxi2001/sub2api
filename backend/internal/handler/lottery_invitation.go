package handler

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"

	iputil "github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const lotteryVisitorCookie = "lottery_visitor"

func lotteryVisitorToken(c *gin.Context) string {
	token, _ := c.Cookie(lotteryVisitorCookie)
	if len(token) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(token); err != nil {
		return ""
	}
	return token
}

// Applied to registration and OAuth callbacks as well as authenticated claims.
func LotteryVisitorContext(c *gin.Context) {
	if token := lotteryVisitorToken(c); token != "" {
		c.Request = c.Request.WithContext(service.ContextWithLotteryVisitor(c.Request.Context(), token))
	}
	c.Next()
}

func (h *LotteryHandler) InviteVisit(c *gin.Context) {
	// Existing users can share links but are not new visitors or new signups.
	if c.GetHeader("Authorization") != "" {
		response.BadRequest(c, "Invitation rewards require a new visitor")
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	if err := c.ShouldBindJSON(&input); err != nil || len(input.Code) != 64 {
		response.BadRequest(c, "Invalid invitation")
		return
	}
	if _, err := hex.DecodeString(input.Code); err != nil {
		response.BadRequest(c, "Invalid invitation")
		return
	}
	token := lotteryVisitorToken(c)
	if token == "" {
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			response.InternalError(c, "Unable to create visitor receipt")
			return
		}
		token = hex.EncodeToString(random[:])
		// Keep the receipt even if the response is lost, so retries cannot award twice.
		http.SetCookie(c.Writer, &http.Cookie{Name: lotteryVisitorCookie, Value: token, Path: "/", MaxAge: 30 * 24 * 60 * 60, HttpOnly: true, Secure: isRequestHTTPS(c), SameSite: http.SameSiteLaxMode})
	}
	address := iputil.GetTrustedClientIP(c)
	country := ""
	if h.geo != nil {
		country = h.geo.Country(c.Request.Context(), address)
	}
	pool, err := h.lottery.RecordInviteVisit(c.Request.Context(), input.Code, token, address, country)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, pool)
}

func (h *LotteryHandler) InviteLink(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	pid, ok := lotteryID(c, "id")
	if !ok {
		return
	}
	summary, err := h.lottery.InviteLink(c.Request.Context(), subject.UserID, pid)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, summary)
}

func (h *LotteryHandler) ClaimInvitation(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	if err := h.lottery.RegisterInvitedUser(c.Request.Context(), subject.UserID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"claimed": true})
}

func (h *LotteryHandler) AdminInvitations(c *gin.Context) {
	page, size := response.ParsePagination(c)
	uid, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	pid, _ := strconv.ParseInt(c.Query("pool_id"), 10, 64)
	data, total, err := h.lottery.Invitations(c.Request.Context(), uid, pid, page, size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, data, int64(total), page, size)
}
