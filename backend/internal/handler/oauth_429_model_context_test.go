package handler

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOAuth429ParsedInputModelAndIdentitySurviveHTTPContext(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"input":"fixture","model":"alias-one","stream":true}`))
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.ClientRequestID, "business-fixture"))
	c.Request.GetBody = nil
	setOpsRequestContext(c, "alias-one", true)
	require.Equal(t, "alias-one", c.Request.Context().Value(ctxkey.Model))
	require.Equal(t, "business-fixture", c.Request.Context().Value(ctxkey.ClientRequestID))
	require.Nil(t, c.Request.GetBody, "streaming bodies do not need to be replayed to retain metadata")
}
