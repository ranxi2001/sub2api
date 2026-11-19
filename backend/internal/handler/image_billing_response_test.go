//go:build unit

package handler

import (
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImageBillingPayloadPreservesRawUsage(t *testing.T) {
	r := service.ImageBillingReceipt{Settled: true, ActualCost: .0201, BillingMode: "image", ImageCount: 2}
	for _, raw := range []string{`{"data":[{"url":"https://example.invalid/image"}],"usage":{"input_tokens":9,"output_tokens":1200}}`, `{"data":[{"b64_json":"fixture"}]}`, `{"data":[],"usage":{"input_tokens":0,"output_tokens":0}}`} {
		b, err := imageEquivalentPayload([]byte(raw), r, true)
		require.NoError(t, err)
		var x map[string]any
		require.NoError(t, json.Unmarshal(b, &x))
		u := x["usage"].(map[string]any)
		require.Equal(t, float64(0), u["input_tokens"])
		require.Equal(t, float64(2010000), u["output_tokens"])
		require.Equal(t, "image", x["billing"].(map[string]any)["billing_mode"])
		if strings.Contains(raw, `"usage"`) {
			require.Contains(t, x, "upstream_usage")
		}
	}
}
func TestImageBillingStreamChargesOnlyFinalImageEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w, err := newImageBillingResponse(c.Writer, true)
	require.NoError(t, err)
	defer w.close()
	raw := "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"first\",\"usage\":{\"output_tokens\":100}}\n\nevent: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"second\",\"usage\":{\"output_tokens\":200}}\n\ndata: [DONE]\n\n"
	_, err = w.WriteString(raw)
	require.NoError(t, err)
	w.Flush()
	require.NotContains(t, rec.Body.String(), "first")
	require.NoError(t, w.settle(service.ImageBillingReceipt{Settled: true, ActualCost: .02, BillingMode: "image", ImageCount: 2}))
	require.Equal(t, 1, strings.Count(rec.Body.String(), `"usage":`))
	require.Equal(t, 2, strings.Count(rec.Body.String(), `"upstream_usage":`))
	require.Contains(t, rec.Body.String(), `"output_tokens":2000000`)
	require.Contains(t, rec.Body.String(), "[DONE]")
}
func TestImageBillingUnsettledResponseNeverExposesImage(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w, err := newImageBillingResponse(c.Writer, false)
	require.NoError(t, err)
	defer w.close()
	_, err = w.WriteString(`{"data":[{"b64_json":"secret-result"}]}`)
	require.NoError(t, err)
	require.Error(t, w.settle(service.ImageBillingReceipt{ActualCost: .01}))
	w.errorResponse("image_billing_unavailable", "not settled")
	w.passthrough()
	require.NotContains(t, rec.Body.String(), "secret-result")
	require.Equal(t, 503, rec.Code)
}

func TestImageBillingEarlyReturnCannotLeakUnsettledSuccess(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w, err := newImageBillingResponse(c.Writer, false)
	require.NoError(t, err)
	defer w.close()
	_, err = w.WriteString(`{"data":[{"b64_json":"unsettled-image"}]}`)
	require.NoError(t, err)
	w.passthrough()
	require.Equal(t, 503, rec.Code)
	require.NotContains(t, rec.Body.String(), "unsettled-image")
}

func TestImageBillingMultilineSSEReplacesUsageOnce(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w, err := newImageBillingResponse(c.Writer, true)
	require.NoError(t, err)
	defer w.close()
	_, err = w.WriteString("event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\n" + "data: \"b64_json\":\"image\",\"usage\":{\"output_tokens\":100},\"billing\":{\"actual_cost\":999}}\n\n")
	require.NoError(t, err)
	require.NoError(t, w.settle(service.ImageBillingReceipt{Settled: true, ActualCost: .01, BillingMode: "token", ImageCount: 1}))
	require.Equal(t, 1, strings.Count(rec.Body.String(), `"usage":`))
	require.Contains(t, rec.Body.String(), `"output_tokens":1000000`)
	require.NotContains(t, rec.Body.String(), "999")
}
