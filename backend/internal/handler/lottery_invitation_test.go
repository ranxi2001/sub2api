package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestLotteryVisitorReceiptValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, token := range []string{"", "not-a-receipt", strings.Repeat("z", 64), strings.Repeat("a", 64)} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.AddCookie(&http.Cookie{Name: lotteryVisitorCookie, Value: token})
		if token == strings.Repeat("a", 64) {
			require.Equal(t, token, lotteryVisitorToken(c))
		} else {
			require.Empty(t, lotteryVisitorToken(c))
		}
	}
}

func TestLotteryInviteVisitRejectsInvalidAndAuthenticatedRequests(t *testing.T) {
	h := NewLotteryHandler(nil)
	router := gin.New()
	router.POST("/visit", h.InviteVisit)
	for _, input := range []struct{ body, authorization string }{{`{"code":"bad"}`, ""}, {`{"code":"` + strings.Repeat("a", 64) + `"}`, "Bearer existing-session"}} {
		req := httptest.NewRequest(http.MethodPost, "/visit", strings.NewReader(input.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", input.authorization)
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		require.Equal(t, http.StatusBadRequest, res.Code)
		require.Empty(t, res.Result().Cookies())
	}
}
