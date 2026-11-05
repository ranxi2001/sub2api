//go:build unit

package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The client history carries a completed tool round trip; a switched account
// must receive exactly the same converted history.
const excelBPSFailoverRequestBody = `{"model":"gpt-6-astra","stream":%t,"input":[` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"list the files"}]},` +
	`{"type":"function_call","call_id":"call_ls","name":"shell","arguments":"{\"cmd\":\"ls\"}"},` +
	`{"type":"function_call_output","call_id":"call_ls","output":"a.txt"}],` +
	`"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]}`

// excelBPSFailoverUpstream answers in call order and records the account of
// every BPS attempt.
type excelBPSFailoverUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
	bodies     [][]byte
	urls       []string
	answer     func(call int) *http.Response
	onDo       func(call int)
}

func (u *excelBPSFailoverUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	u.mu.Lock()
	call := len(u.accountIDs)
	u.accountIDs = append(u.accountIDs, accountID)
	u.bodies = append(u.bodies, body)
	u.urls = append(u.urls, req.URL.String())
	u.mu.Unlock()
	if u.onDo != nil {
		u.onDo(call)
	}
	return u.answer(call), nil
}

func (u *excelBPSFailoverUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

func excelBPS429(retryAfter string) *http.Response {
	header := http.Header{"Content-Type": {"application/json"}}
	if retryAfter != "" {
		header.Set("Retry-After", retryAfter)
	}
	return &http.Response{StatusCode: http.StatusTooManyRequests, Header: header,
		Body: io.NopCloser(strings.NewReader(`{"error":{"code":"basispoints_rate_limited","message":"PRIVATE_UPSTREAM workspace detail"}}`))}
}

func excelBPSCompleted() *http.Response {
	wire := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_bps_failover\",\"status\":\"completed\",\"model\":\"gpt-6-astra\"," +
		"\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}
}

// excelBPSFailoverAccountRepo also answers the no-account diagnosis query.
type excelBPSFailoverAccountRepo struct {
	openAIImagesFailoverAccountRepo
}

func (r excelBPSFailoverAccountRepo) ListModelAvailabilityCandidates(_ context.Context, _ *int64, platforms []string, _ bool) ([]service.Account, error) {
	var out []service.Account
	for _, platform := range platforms {
		out = append(out, r.accountsForPlatform(platform)...)
	}
	return out, nil
}

// loadBatch selects the production default (load-aware) scheduling path;
// otherwise the simple priority path is used.
func newExcelBPSFailoverTestHandler(t *testing.T, upstream service.HTTPUpstream, loadBatch bool) *OpenAIGatewayHandler {
	t.Helper()
	var accounts []service.Account
	for _, id := range []int64{1, 2} {
		accounts = append(accounts, service.Account{
			ID: id, Name: fmt.Sprintf("bps-account-%d", id), Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3131},
			Credentials: map[string]any{"access_token": fmt.Sprintf("token-%d", id), "chatgpt_account_id": fmt.Sprintf("chatgpt-%d", id)},
			Extra:       map[string]any{"openai_excel_bps": true},
		})
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	concurrencyService := service.NewConcurrencyService(nil)
	var schedulingConcurrency *service.ConcurrencyService
	if loadBatch {
		cfg.Gateway.Scheduling.LoadBatchEnabled = true
		schedulingConcurrency = concurrencyService
	}
	gatewayService := service.NewOpenAIGatewayService(
		excelBPSFailoverAccountRepo{openAIImagesFailoverAccountRepo{accounts: accounts}},
		nil, nil, nil, nil, nil, nil, nil,
		cfg,
		nil, schedulingConcurrency, nil, nil, nil,
		upstream,
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingService.Stop)
	handler := NewOpenAIGatewayHandler(
		gatewayService,
		concurrencyService,
		billingService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil,
		cfg,
	)
	handler.maxAccountSwitches = 10
	return handler
}

func newExcelBPSFailoverTestContext(ctx context.Context, stream bool) (*gin.Context, *httptest.ResponseRecorder) {
	groupID := int64(3131)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(fmt.Sprintf(excelBPSFailoverRequestBody, stream))))
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:               99,
		GroupID:          &groupID,
		Group:            &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
		User:             &service.User{ID: 100},
		AllowLongContext: true,
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 0})
	return c, rec
}

// Issue #146: A -> B within the first request, then B directly while A cools.
func TestOpenAIResponsesExcelBPS429SwitchesAccountAndCoolsIt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		loadBatch, stream bool
	}{{false, false}, {false, true}, {true, false}, {true, true}} {
		stream := tc.stream
		t.Run(fmt.Sprintf("load_batch=%t/stream=%t", tc.loadBatch, stream), func(t *testing.T) {
			upstream := &excelBPSFailoverUpstream{answer: func(call int) *http.Response {
				if call == 0 {
					return excelBPS429("30")
				}
				return excelBPSCompleted()
			}}
			handler := newExcelBPSFailoverTestHandler(t, upstream, tc.loadBatch)

			c, rec := newExcelBPSFailoverTestContext(context.Background(), stream)
			handler.Responses(c)

			calls := upstream.calls()
			require.Len(t, calls, 2)
			throttled, healthy := calls[0], calls[1]
			require.NotEqual(t, throttled, healthy, "the throttled account must not be retried in this request")
			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Body.String(), "done")
			require.NotContains(t, rec.Body.String(), "PRIVATE_UPSTREAM")
			require.NotContains(t, rec.Body.String(), "basispoints_rate_limited")
			for _, url := range upstream.urls {
				require.Equal(t, basispoints.ResponsesURL, url)
			}
			require.NotEmpty(t, gjson.GetBytes(upstream.bodies[0], "input").Raw)
			require.Equal(t, gjson.GetBytes(upstream.bodies[0], "input").Raw, gjson.GetBytes(upstream.bodies[1], "input").Raw,
				"messages, tool calls and tool outputs must survive the account switch unchanged")
			rawEvents, ok := c.Get(service.OpsUpstreamErrorsKey)
			require.True(t, ok)
			events, ok := rawEvents.([]*service.OpsUpstreamErrorEvent)
			require.True(t, ok)
			require.Len(t, events, 1)
			require.Equal(t, "failover", events[0].Kind)
			require.Equal(t, throttled, events[0].AccountID)
			require.Equal(t, http.StatusTooManyRequests, events[0].UpstreamStatusCode)

			c, rec = newExcelBPSFailoverTestContext(context.Background(), stream)
			handler.Responses(c)

			require.Equal(t, []int64{throttled, healthy, healthy}, upstream.calls(), "the cooling account is skipped by the next request")
			require.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestOpenAIResponsesExcelBPS429OnEveryAccountReturnsRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, retryAfter, wantRetryAfter string
		stream, loadBatch                bool
	}{
		{name: "json", retryAfter: "30", wantRetryAfter: "30"},
		{name: "stream request", retryAfter: "30", wantRetryAfter: "30", stream: true},
		{name: "load-aware scheduling", retryAfter: "30", wantRetryAfter: "30", loadBatch: true},
		{name: "invalid retry after", retryAfter: "soon"},
		{name: "retry after beyond a week", retryAfter: "999999"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &excelBPSFailoverUpstream{answer: func(int) *http.Response { return excelBPS429(tc.retryAfter) }}
			handler := newExcelBPSFailoverTestHandler(t, upstream, tc.loadBatch)

			c, rec := newExcelBPSFailoverTestContext(context.Background(), tc.stream)
			handler.Responses(c)

			calls := upstream.calls()
			require.Len(t, calls, 2)
			require.NotEqual(t, calls[0], calls[1])
			require.Equal(t, http.StatusTooManyRequests, rec.Code)
			require.Equal(t, "rate_limit_error", gjson.Get(rec.Body.String(), "error.type").String())
			require.Equal(t, "basispoints_rate_limited", gjson.Get(rec.Body.String(), "error.code").String())
			require.NotContains(t, rec.Body.String(), "PRIVATE_UPSTREAM")
			require.NotContains(t, rec.Body.String(), "workspace")
			require.Equal(t, tc.wantRetryAfter, rec.Header().Get("Retry-After"))

			// Both accounts cool down: the next request is answered without an upstream call.
			c, rec = newExcelBPSFailoverTestContext(context.Background(), tc.stream)
			handler.Responses(c)

			require.Len(t, upstream.calls(), 2)
			require.Equal(t, http.StatusTooManyRequests, rec.Code)
			require.Equal(t, "rate_limit_error", gjson.Get(rec.Body.String(), "error.type").String())
			require.Contains(t, rec.Body.String(), "rate-limited")
		})
	}
}

func TestOpenAIResponsesExcelBPS429StopsWhenClientCancels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := &excelBPSFailoverUpstream{
		answer: func(int) *http.Response { return excelBPS429("30") },
		onDo:   func(int) { cancel() },
	}
	handler := newExcelBPSFailoverTestHandler(t, upstream, false)
	c, rec := newExcelBPSFailoverTestContext(ctx, false)

	handler.Responses(c)

	require.Len(t, upstream.calls(), 1, "a gone client must not be replayed on another account")
	require.Equal(t, statusClientClosedRequest, c.Writer.Status())
	require.Zero(t, rec.Body.Len())
}
