//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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

type imageSettlementHTTPRepo struct {
	service.UsageBillingRepository
	service.DurableUsageBillingRepository
	event      service.UsageBillingEvent
	settlement int
}

func (r *imageSettlementHTTPRepo) StageUsageBillingForReceipt(_ context.Context, cmd *service.UsageBillingCommand, usage *service.UsageLog) (bool, error) {
	r.event = service.UsageBillingEvent{Command: *cmd, Usage: *usage}
	return true, nil
}

func (r *imageSettlementHTTPRepo) ApplyStagedUsageBilling(context.Context, service.UsageBillingEventKey) (*service.UsageBillingRecovery, error) {
	r.settlement++
	return &service.UsageBillingRecovery{Event: r.event, Result: service.UsageBillingApplyResult{Applied: true}, Settled: true}, nil
}

type imageSettlementUpstream struct {
	service.HTTPUpstream
	contentType, body string
	calls             int
}

func (u *imageSettlementUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {u.contentType}, "X-Request-Id": {"image-settlement-http"}},
		Body:       io.NopCloser(strings.NewReader(u.body)),
	}, nil
}

type imageSettlementUserRepo struct {
	service.UserRepository
	user *service.User
}

func (r imageSettlementUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	return r.user, nil
}

func runImageSettlementHTTP(t *testing.T, contentType, body string) *httptest.ResponseRecorder {
	return runImageSettlementHTTPWithCount(t, contentType, body, 1)
}

func runImageSettlementHTTPWithCount(t *testing.T, contentType, body string, count int) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Runtime.Role = config.RuntimeRoleGateway
	groupID := int64(31)
	price := .01
	user := &service.User{ID: 10, Balance: 100, Status: service.StatusActive}
	group := &service.Group{
		ID: groupID, Platform: service.PlatformOpenAI, RateMultiplier: 1, AllowImageGeneration: true,
		ModelPricing: []service.ChannelModelPricing{{
			Models: []string{"gpt-image-2"}, BillingMode: service.BillingModeToken,
			InputPrice: new(float64), OutputPrice: new(float64), ImageMinimumCharge: &price,
		}},
	}
	key := &service.APIKey{ID: 20, UserID: user.ID, User: user, GroupID: &groupID, Group: group, Quota: 100}
	account := service.Account{ID: 30, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{groupID},
		Credentials: map[string]any{"api_key": "fixture", "base_url": "https://fixture.example/v1"}}
	accounts := openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}
	users := imageSettlementUserRepo{user: user}
	billing := &imageSettlementHTTPRepo{}
	upstream := &imageSettlementUpstream{contentType: contentType, body: body}
	cache := service.NewBillingCacheService(nil, users, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(cache.Stop)
	pricing := service.NewBillingService(cfg, nil)
	gateway := service.NewOpenAIGatewayService(accounts, nil, nil, billing, users, nil, nil, nil, cfg,
		nil, nil, pricing, nil, cache, upstream, nil, nil, nil, service.NewModelPricingResolver(nil, pricing), nil, nil, nil, nil)
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), cache,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"model":"gpt-image-2","prompt":"fixture","stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Image-Usage", "billing")
	c.Set(string(middleware.ContextKeyAPIKey), key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: user.ID})
	h.Images(c)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, 1, billing.settlement)
	require.Equal(t, .01*float64(count), billing.event.Command.BalanceCost)
	require.Equal(t, .01*float64(count), billing.event.Command.APIKeyQuotaCost)
	require.Equal(t, count, billing.event.Usage.ImageCount)
	return rec
}

func imageBillingSSEPayloads(t *testing.T, body string) []gjson.Result {
	t.Helper()
	var payloads []gjson.Result
	for _, block := range strings.Split(body, "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				require.True(t, gjson.Valid(data), data)
				payloads = append(payloads, gjson.Parse(data))
			}
		}
	}
	return payloads
}

func TestImageBillingResponsesCompletedImageEvents(t *testing.T) {
	const item = `{"type":"image_generation_call","id":"image-1","result":"ZmluYWw="}`
	itemDone := `data: {"type":"response.output_item.done","item":` + item + `,"usage":{"output_tokens":100}}` + "\n\n"
	for _, kind := range []string{"response.completed", "response.done", "response.output_item.done"} {
		for _, itemFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/item_first=%t", kind, itemFirst), func(t *testing.T) {
				body := ""
				if itemFirst {
					body = itemDone
				}
				if kind == "response.output_item.done" {
					body += itemDone
				} else {
					body += fmt.Sprintf("data: {\"type\":%q,\"response\":{\"output\":[%s]},\"usage\":{\"output_tokens\":100}}\n\n", kind, item)
				}
				completedIndex := 0
				if itemFirst {
					completedIndex = 1
				}
				// Neither an empty Responses terminal nor a partial with bytes
				// may take the completed image's authoritative request usage.
				body += "data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"
				body += "data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"unfinished\"}\n\n"
				rec := runImageSettlementHTTP(t, "text/event-stream", body)
				payloads := imageBillingSSEPayloads(t, rec.Body.String())
				require.Len(t, payloads, completedIndex+3)
				var usages int
				for i, payload := range payloads {
					if i == completedIndex {
						require.Equal(t, kind, payload.Get("type").String())
						imagePath := "response.output.0.result"
						if kind == "response.output_item.done" {
							imagePath = "item.result"
						}
						require.Equal(t, "ZmluYWw=", payload.Get(imagePath).String())
						require.EqualValues(t, 1_000_000, payload.Get("usage.output_tokens").Int())
						require.Equal(t, .01, payload.Get("billing.actual_cost").Float())
						require.EqualValues(t, 100, payload.Get("upstream_usage.output_tokens").Int())
					} else {
						require.False(t, payload.Get("usage").Exists())
						require.False(t, payload.Get("billing").Exists())
					}
					if payload.Get("usage").Exists() {
						usages++
					}
				}
				require.Equal(t, 1, usages)
				require.Equal(t, "unfinished", payloads[len(payloads)-1].Get("b64_json").String())
			})
		}
	}
}

func TestImageBillingResponsesWithoutCompletedImage(t *testing.T) {
	for _, raw := range []string{
		`{"type":"response.completed","response":{"output":[]}}`,
		`{"type":"response.done","response":{"output":[{"type":"message","content":[]}]}}`,
		`{"type":"response.completed","response":{"output":[{"type":"image_generation_call","partial_image_index":0,"result":"unfinished"}]}}`,
		`{"type":"image_generation.partial_image","b64_json":"unfinished"}`,
	} {
		t.Run(gjson.Get(raw, "type").String()+"/"+raw, func(t *testing.T) {
			rec := runImageSettlementHTTPWithCount(t, "text/event-stream", "data: "+raw+"\n\n", 0)
			payloads := imageBillingSSEPayloads(t, rec.Body.String())
			require.Len(t, payloads, 1)
			require.Equal(t, "image_usage_unavailable", payloads[0].Get("error.code").String())
			require.False(t, payloads[0].Get("usage").Exists())
			require.False(t, payloads[0].Get("billing").Exists())
			require.NotContains(t, rec.Body.String(), "unfinished")
		})
	}
}

func TestImageBillingStreamJSONFallback(t *testing.T) {
	for _, contentType := range []string{"application/json", "text/event-stream"} {
		t.Run(contentType, func(t *testing.T) {
			rec := runImageSettlementHTTP(t, contentType, `{"created":1710000009,"data":[{"b64_json":"ZmluYWw="}],"usage":{"input_tokens":10,"output_tokens":18}}`)
			require.Equal(t, "application/json", rec.Result().Header.Get("Content-Type"))
			require.True(t, json.Valid(rec.Body.Bytes()), rec.Body.String())
			result := gjson.ParseBytes(rec.Body.Bytes())
			require.Equal(t, "ZmluYWw=", result.Get("data.0.b64_json").String())
			require.EqualValues(t, 1_000_000, result.Get("usage.output_tokens").Int())
			require.EqualValues(t, 18, result.Get("upstream_usage.output_tokens").Int())
			require.Equal(t, .01, result.Get("billing.actual_cost").Float())
		})
	}
}

func TestImageBillingJSONFallbackIgnoresKeepaliveComments(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w, err := newImageBillingResponse(c.Writer, true)
	require.NoError(t, err)
	defer w.close()
	w.Header().Set("Content-Type", "text/event-stream")
	for _, part := range []string{":\n\n", "{\n", ":\n\n", `"data":[{"b64_json":"ZmluYWw="}]`, "\n}", ":\n\n"} {
		_, err = w.WriteString(part)
		require.NoError(t, err)
		w.Flush()
		require.Empty(t, rec.Body.String(), "no SSE header or comment may precede JSON delivery")
		require.False(t, w.ResponseWriter.Written())
	}
	require.NoError(t, w.settle(service.ImageBillingReceipt{Settled: true, ActualCost: .01, ImageCount: 1}))
	require.Equal(t, "application/json", rec.Result().Header.Get("Content-Type"))
	require.True(t, json.Valid(rec.Body.Bytes()), rec.Body.String())
	require.Equal(t, "ZmluYWw=", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
	require.EqualValues(t, 1_000_000, gjson.Get(rec.Body.String(), "usage.output_tokens").Int())
}

func TestImageBillingUsageRemainsOnCompletedAfterPartial(t *testing.T) {
	rec := runImageSettlementHTTP(t, "text/event-stream",
		"event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"first\",\"usage\":{\"output_tokens\":100}}\n\n"+
			"event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"partial_image_index\":0,\"b64_json\":\"unfinished-second\"}\n\n"+
			"event: error\ndata: {\"error\":{\"message\":\"second image failed\"}}\n\n")
	require.Equal(t, "text/event-stream", rec.Result().Header.Get("Content-Type"))
	var names []string
	var payloads []gjson.Result
	for _, block := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n") {
		name, data, ok := strings.Cut(block, "\ndata: ")
		if !ok {
			continue
		}
		names = append(names, strings.TrimPrefix(name, "event: "))
		require.True(t, gjson.Valid(data), data)
		payloads = append(payloads, gjson.Parse(data))
	}
	require.Equal(t, []string{"image_generation.completed", "image_generation.partial_image", "error"}, names)
	require.Equal(t, "first", payloads[0].Get("b64_json").String())
	require.EqualValues(t, 1_000_000, payloads[0].Get("usage.output_tokens").Int())
	require.EqualValues(t, 100, payloads[0].Get("upstream_usage.output_tokens").Int())
	require.Equal(t, .01, payloads[0].Get("billing.actual_cost").Float())
	require.Equal(t, "unfinished-second", payloads[1].Get("b64_json").String())
	require.Equal(t, "second image failed", payloads[2].Get("error.message").String())
	for _, event := range payloads[1:] {
		require.False(t, event.Get("usage").Exists())
		require.False(t, event.Get("billing").Exists())
	}
}
