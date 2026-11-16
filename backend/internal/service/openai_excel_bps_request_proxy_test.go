package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// A configured request pool must take precedence even when the old session
// pool switch is off. Observing the forwarding boundary catches direct leaks.
func TestExcelBPSRequestProxyNeverSendsOutsideSelectedPool(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[]}}\n\n"))}}
	svc := openAIClientToolsTestService(upstream)
	account := excelAccount()
	account.Extra["openai_excel_bps_proxy_mode"] = "request"
	account.Extra["openai_excel_bps_proxy_ids"] = []int64{987654321}
	account.Extra["openai_excel_bps_mihomo"] = false
	account.Proxy = &Proxy{Protocol: "http", Host: "unused.example", Port: 8080}
	body := []byte(`{"model":"gpt-5.6-sol","input":"test","stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	_, err := svc.Forward(context.Background(), c, account, body)
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "basispoints_proxy_unavailable")
	require.Nil(t, upstream.lastReq, "an unavailable selected pool must send neither directly nor through the old account proxy")
}
