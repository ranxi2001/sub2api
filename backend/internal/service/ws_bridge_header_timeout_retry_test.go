package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func bridgeRetryResponse(id string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(fmt.Sprintf(
			`data: {"type":"response.completed","response":{"id":"%s","status":"completed","usage":{"input_tokens":100,"output_tokens":50}}}`+"\n\n", id)))}
}

func bridgeRetryContext(t *testing.T) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	return c
}

func bridgeRetryAccount() *Account {
	return &Account{ID: 91, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1}
}

func bridgeRetryService(upstream HTTPUpstream, retries int) *OpenAIGatewayService {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIResponseHeaderTimeout = 1
	settings := NewSettingService(&panelRateLimitSettingRepo{values: map[string]string{
		SettingKeyOpenAIResponseHeaderTimeoutSettings: fmt.Sprintf(
			`{"enabled":true,"seconds":1,"max_retries":%d}`, retries),
	}}, cfg)
	return &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, settingService: settings}
}

func TestWSBridgeHeaderTimeoutRetryPreservesTurnAndBillsOnce(t *testing.T) {
	for _, retries := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("budget_%d", retries), func(t *testing.T) {
			timeoutAttempts := retries
			if timeoutAttempts == 0 {
				timeoutAttempts = 1
			}
			errs := make([]error, timeoutAttempts+2)
			responses := make([]*http.Response, timeoutAttempts+2)
			for i := 0; i < retries; i++ {
				errs[i] = &openAIHTTPPhaseTimeout{phase: "response_header_timeout"}
			}
			if retries == 0 {
				errs[0] = &openAIHTTPPhaseTimeout{phase: "response_header_timeout"}
			}
			for i := retries; i < len(responses); i++ {
				responses[i] = bridgeRetryResponse(fmt.Sprintf("resp_retry_%d", i))
			}
			upstream := &httpUpstreamSequenceRecorder{errs: errs, responses: responses}
			svc := bridgeRetryService(upstream, retries)
			c := bridgeRetryContext(t)
			// Include function-call context and opaque account-owned state. The
			// bridge's prepared body must be identical across both sends.
			payload := []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_prior","input":[{"type":"function_call","name":"inspect","call_id":"call_one","arguments":"{}"},{"type":"function_call_output","call_id":"call_one","output":"synthetic"}],"prompt_cache_key":"synthetic-cache"}`)
			var writes [][]byte
			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(t.Context(), c, bridgeRetryAccount(), "synthetic", payload, len(payload),
				"gpt-5.1", "", "", "", "", 20, func(b []byte) error {
					writes = append(writes, append([]byte(nil), b...))
					return nil
				})
			if retries == 0 {
				require.Error(t, err)
				require.Nil(t, result)
				require.Equal(t, 1, upstream.callCount, "zero disables same-account retries")
				require.Len(t, writes, 1)
				require.Contains(t, string(writes[0]), "retry is disabled by settings")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, retries, result.HeaderTimeoutRetries)
			require.Equal(t, timeoutAttempts+1, upstream.callCount)
			for i := 1; i < upstream.callCount; i++ {
				require.Equal(t, upstream.bodies[0], upstream.bodies[i])
				require.Equal(t, upstream.reqs[0].URL, upstream.reqs[i].URL)
				require.Equal(t, upstream.reqs[0].Header.Get("Authorization"), upstream.reqs[i].Header.Get("Authorization"))
			}
			require.Len(t, writes, 1, "the failed attempt must not send a client error")
			require.Equal(t, "response.completed", gjson.GetBytes(writes[0], "type").String())

			// Exercise the real usage recorder with this forwarded result, rather
			// than manufacturing the retry count in a billing-only unit test.
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			billing := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			input := &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 10, GroupID: i64p(11),
				Group: &Group{ID: 11, RateMultiplier: 1.2}}, User: &User{ID: 20}, Account: bridgeRetryAccount()}
			require.NoError(t, billing.RecordUsage(t.Context(), input))
			doubledActual, doubledTotal := repo.lastLog.ActualCost, repo.lastLog.TotalCost
			require.Positive(t, doubledActual)
			baseline := *result
			baseline.RequestID = "resp_baseline"
			baseline.HeaderTimeoutRetries = 0
			input.Result = &baseline
			require.NoError(t, billing.RecordUsage(t.Context(), input))
			factor := float64(1 + retries)
			require.InDelta(t, repo.lastLog.ActualCost*factor, doubledActual, 1e-9)
			require.InDelta(t, repo.lastLog.TotalCost*factor, doubledTotal, 1e-9)

			next, err := svc.proxyOpenAIWSHTTPBridgeTurn(t.Context(), c, bridgeRetryAccount(), "synthetic", payload, len(payload),
				"gpt-5.1", "", "", "", "", 21, func([]byte) error { return nil })
			require.NoError(t, err)
			require.Equal(t, 0, next.HeaderTimeoutRetries, "surcharge must not leak into another turn")
			require.Equal(t, timeoutAttempts+2, upstream.callCount)
		})
	}
}

func TestWSBridgeHeaderTimeoutRetryIsBoundedAndDoesNotRetryOtherPhases(t *testing.T) {
	for _, tc := range []struct {
		phase string
		calls int
	}{{"response_header_timeout", 2}, {"upload_timeout", 1}, {"pre_output_budget", 1}} {
		t.Run(tc.phase, func(t *testing.T) {
			err := &openAIHTTPPhaseTimeout{phase: tc.phase}
			upstream := &httpUpstreamSequenceRecorder{errs: []error{err, err, err}}
			svc := bridgeRetryService(upstream, 1)
			payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)
			writes := 0
			result, got := svc.proxyOpenAIWSHTTPBridgeTurn(t.Context(), bridgeRetryContext(t), bridgeRetryAccount(), "synthetic",
				payload, len(payload), "gpt-5.1", "", "", "", "", 2, func([]byte) error { writes++; return nil })
			require.Error(t, got)
			require.Nil(t, result)
			require.Equal(t, tc.calls, upstream.callCount)
			require.Equal(t, 1, writes)
		})
	}
}

type bridgeDisconnectAfterTimeout struct {
	httpUpstreamSequenceRecorder
	disconnected chan struct{}
}

func (u *bridgeDisconnectAfterTimeout) Do(req *http.Request, proxy string, accountID int64, concurrency int) (*http.Response, error) {
	response, err := u.httpUpstreamSequenceRecorder.Do(req, proxy, accountID, concurrency)
	close(u.disconnected)
	return response, err
}

func TestWSBridgeHeaderTimeoutDoesNotRetryDisconnectedClient(t *testing.T) {
	upstream := &bridgeDisconnectAfterTimeout{httpUpstreamSequenceRecorder: httpUpstreamSequenceRecorder{
		errs: []error{&openAIHTTPPhaseTimeout{phase: "response_header_timeout"}},
	}, disconnected: make(chan struct{})}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	ctx := context.WithValue(t.Context(), openAIWSDisconnectKey{}, (<-chan struct{})(upstream.disconnected))
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)
	_, err := svc.proxyOpenAIWSHTTPBridgeTurn(ctx, bridgeRetryContext(t), bridgeRetryAccount(), "synthetic", payload, len(payload),
		"gpt-5.1", "", "", "", "", 1, func([]byte) error { return io.EOF })
	require.Error(t, err)
	require.Equal(t, 1, upstream.callCount)
}

func TestWSBridgeHeaderTimeoutRetryDoesNotLoseSurchargeByFailingOverAgain(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			upstream := &httpUpstreamSequenceRecorder{
				errs: []error{&openAIHTTPPhaseTimeout{phase: "response_header_timeout"}, nil},
				responses: []*http.Response{nil, {StatusCode: status, Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader(`{"error":{"message":"synthetic refusal"}}`))}},
			}
			svc := bridgeRetryService(upstream, 1)
			payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)
			_, err := svc.proxyOpenAIWSHTTPBridgeTurn(t.Context(), bridgeRetryContext(t), bridgeRetryAccount(), "synthetic", payload, len(payload),
				"gpt-5.1", "", "", "", "", 1, func([]byte) error { return nil })
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
			require.Equal(t, 2, upstream.callCount)
		})
	}
}
