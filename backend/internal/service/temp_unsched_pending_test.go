//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCompatErrorPipelineCountsEachResponseOnce(t *testing.T) {
	repo := &tempUnschedPendingRepo{}
	limits, counter := newCountedTempUnschedService(repo)
	gateway := &OpenAIGatewayService{rateLimitService: limits}
	account := tempUnschedTimingAccount(map[string]any{
		"error_code": float64(400), "keywords": []any{"temporary fixture"},
		"duration_minutes": float64(10), "scope": "model", "trigger_count": float64(3),
	})
	body := []byte(`{"error":{"message":"temporary fixture","type":"invalid_request_error"}}`)
	for attempt := 1; attempt <= 3; attempt++ {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		resp := &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}
		failover := gateway.failoverOpenAIUpstreamHTTPError(c.Request.Context(), c, account, resp, body, "temporary fixture", "gpt-5.4")
		if attempt < 3 {
			require.Nil(t, failover)
			_, err := gateway.handleChatCompletionsErrorResponse(resp, c, account, "gpt-5.4")
			require.Error(t, err)
			var unexpected *UpstreamFailoverError
			require.NotErrorAs(t, err, &unexpected)
			require.Empty(t, repo.modelReasons, "one response cannot satisfy the multi-error threshold")
		} else {
			require.NotNil(t, failover)
			require.Len(t, repo.modelReasons, 1)
		}
		require.Equal(t, attempt, counter.hits, "same upstream response must be counted once across compatibility handlers")
	}
}

type tempUnschedPendingRepo struct {
	recordingTempUnschedRepo
	setErrorCalls int
	lastError     string
}

func (r *tempUnschedPendingRepo) SetError(_ context.Context, _ int64, msg string) error {
	r.setErrorCalls++
	r.lastError = msg
	return nil
}

type countingTempUnschedTrigger struct {
	inner       *memoryTempUnschedTriggerCounter
	hits        int
	resetEpochs []string
}

func (c *countingTempUnschedTrigger) Increment(ctx context.Context, key string, windowMinutes int) (int64, string, error) {
	c.hits++
	return c.inner.Increment(ctx, key, windowMinutes)
}

func (c *countingTempUnschedTrigger) Reset(ctx context.Context, key, epoch string) error {
	c.resetEpochs = append(c.resetEpochs, epoch)
	return c.inner.Reset(ctx, key, epoch)
}

func newCountedTempUnschedService(repo *tempUnschedPendingRepo) (*RateLimitService, *countingTempUnschedTrigger) {
	limits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	counter := &countingTempUnschedTrigger{inner: newMemoryTempUnschedTriggerCounter(time.Now)}
	limits.SetTempUnschedTriggerCounter(counter)
	return limits, counter
}

func TestFastPathThenGeneralHandler_CountsOne502UntilThreshold(t *testing.T) {
	repo := &tempUnschedPendingRepo{}
	limits, counter := newCountedTempUnschedService(repo)
	gateway := &OpenAIGatewayService{rateLimitService: limits}
	account := tempUnschedTimingAccount(map[string]any{
		"error_code":             float64(http.StatusBadGateway),
		"keywords":               []any{"bad gateway"},
		"duration_minutes":       float64(10),
		"scope":                  "model",
		"trigger_count":          float64(2),
		"trigger_window_minutes": float64(5),
	})
	body := []byte(`{"error":{"message":"bad gateway","type":"upstream_error"}}`)

	disabled := gateway.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, body, "gpt-5.4")
	require.False(t, disabled)
	require.Equal(t, 1, counter.hits, "fastpath and the general handler must share one count")
	require.Empty(t, repo.modelReasons)
	require.Zero(t, repo.setErrorCalls)
	require.Empty(t, repo.reasons)

	disabled = gateway.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, body, "gpt-5.4")
	require.True(t, disabled)
	require.Equal(t, 2, counter.hits)
	require.Len(t, repo.modelReasons, 1)
	require.Zero(t, repo.setErrorCalls)
	require.NotEmpty(t, counter.resetEpochs)
	require.NotEmpty(t, counter.resetEpochs[0])
}

func TestPendingModel401_DoesNotDisableWholeAccount(t *testing.T) {
	repo := &tempUnschedPendingRepo{}
	limits, counter := newCountedTempUnschedService(repo)
	account := tempUnschedTimingAccount(map[string]any{
		"error_code":             float64(http.StatusUnauthorized),
		"keywords":               []any{"unauthorized"},
		"duration_minutes":       float64(10),
		"scope":                  "model",
		"trigger_count":          float64(3),
		"trigger_window_minutes": float64(5),
	})
	body := []byte(`{"error":{"message":"unauthorized","type":"invalid_request_error"}}`)

	disabled := limits.HandleUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, body, "gpt-5.4")
	require.False(t, disabled)
	require.Zero(t, repo.setErrorCalls)
	require.Empty(t, repo.modelReasons)
	require.Equal(t, 1, counter.hits)

	gateway := &OpenAIGatewayService{rateLimitService: limits}
	disabled = gateway.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, body, "gpt-5.4")
	require.False(t, disabled)
	require.Zero(t, repo.setErrorCalls)
	require.Empty(t, repo.modelReasons)
	require.Equal(t, 2, counter.hits, "the fastpath must not count the same 401 again in general handling")
}

func TestCredential401_StillDisablesDespitePendingModelRule(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{"error":{"code":"token_revoked","message":"unauthorized"}}`),
		[]byte(`{"error":{"code":"token_invalidated","message":"unauthorized"}}`),
		[]byte(`{"detail":"Unauthorized"}`),
	}
	for _, body := range bodies {
		repo := &tempUnschedPendingRepo{}
		limits, counter := newCountedTempUnschedService(repo)
		account := tempUnschedTimingAccount(map[string]any{
			"error_code":             float64(http.StatusUnauthorized),
			"keywords":               []any{"unauthorized"},
			"duration_minutes":       float64(10),
			"scope":                  "model",
			"trigger_count":          float64(3),
			"trigger_window_minutes": float64(5),
		})

		disabled := limits.HandleUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, body, "gpt-5.4")
		require.True(t, disabled, string(body))
		require.Equal(t, 1, repo.setErrorCalls, string(body))
		require.Empty(t, repo.modelReasons)
		require.Zero(t, counter.hits, string(body))
	}
}

func TestOpenAIWSAfterOutput_Generic500AndExplicit502NeedAfterStream(t *testing.T) {
	repo := &tempUnschedPendingRepo{}
	limits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	gateway := &OpenAIGatewayService{rateLimitService: limits}
	before := func(status int, keyword string) *Account {
		return tempUnschedTimingAccount(map[string]any{
			"error_code":       float64(status),
			"keywords":         []any{keyword},
			"duration_minutes": float64(5),
			"scope":            "model",
			"stream_timing":    TempUnschedStreamBefore,
		})
	}
	after := func(status int, keyword string) *Account {
		account := before(status, keyword)
		rules := account.Credentials["temp_unschedulable_rules"].([]any)
		rule := rules[0].(map[string]any)
		rule["stream_timing"] = TempUnschedStreamAfter
		return account
	}

	upstreamError := []byte(`{"type":"error","error":{"type":"upstream_error","code":"upstream_error","message":"upstream_error"}}`)
	require.Equal(t, http.StatusInternalServerError, openAIWSPayloadTransientStatus(upstreamError))
	outputCtx := openAIWSForwardStreamPhaseContext(context.Background(), true, upstreamError)
	require.Equal(t, tempUnschedPhaseMidStreamOptIn, tempUnschedStreamPhaseFrom(outputCtx))

	gateway.handleOpenAIWSErrorEventTransientFailure(outputCtx, before(http.StatusInternalServerError, "upstream_error"), "gpt-5.4", nil, upstreamError)
	require.Empty(t, repo.modelReasons, "a before_stream rule must not cool a generic upstream_error after output started")

	gateway.handleOpenAIWSErrorEventTransientFailure(outputCtx, after(http.StatusInternalServerError, "upstream_error"), "gpt-5.4", nil, upstreamError)
	require.Len(t, repo.modelReasons, 1)
	repo.modelReasons = nil

	failed := []byte(`{"type":"response.failed","response":{"error":{"status_code":502,"message":"bad gateway"}}}`)
	require.Equal(t, http.StatusBadGateway, openAIWSPayloadTransientStatus(failed))
	failedCtx := openAIWSForwardStreamPhaseContext(context.Background(), true, failed)
	require.Equal(t, tempUnschedPhaseMidStreamOptIn, tempUnschedStreamPhaseFrom(failedCtx))
	require.True(t, gateway.handleOpenAIWSFailureAccountSideEffects(failedCtx, before(http.StatusBadGateway, "bad gateway"), "gpt-5.4", nil, failed))
	require.Empty(t, repo.modelReasons, "an explicit 502 response.failed after output must not match before_stream")

	require.True(t, gateway.handleOpenAIWSFailureAccountSideEffects(context.Background(), before(http.StatusBadGateway, "bad gateway"), "gpt-5.4", nil, failed))
	require.Len(t, repo.modelReasons, 1, "the same before_stream rule still matches before output starts")
}

func TestMemoryTempUnschedTriggerDelayedResetKeepsNewRound(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	counter := newMemoryTempUnschedTriggerCounter(func() time.Time { return now })
	ctx := context.Background()
	key := "temp_unsched_trigger:3:0:model:gpt-5.4"

	_, oldEpoch, err := counter.Increment(ctx, key, 5)
	require.NoError(t, err)
	_, oldEpoch, err = counter.Increment(ctx, key, 5)
	require.NoError(t, err)
	require.NoError(t, counter.Reset(ctx, key, oldEpoch))

	count, newEpoch, err := counter.Increment(ctx, key, 5)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	require.NotEqual(t, oldEpoch, newEpoch)

	require.NoError(t, counter.Reset(ctx, key, oldEpoch))
	count, _, err = counter.Increment(ctx, key, 5)
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
}
