//go:build unit

package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBEpusdtCallbackOrderLookupAndAcknowledgment(t *testing.T) {
	require.Equal(t, "merchant-order", extractOutTradeNo(`{"order_id":"merchant-order","trade_id":"upstream-order"}`, payment.TypeBEpusdt))
	require.Empty(t, extractOutTradeNo(`{"order_id":123}`, payment.TypeBEpusdt))
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	writeSuccessResponse(c, payment.TypeBEpusdt)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "success", w.Body.String())
}
