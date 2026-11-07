//go:build unit

package service

import (
	"bytes"
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

// An unclassified xAI 403 carries its reason only in the response body, so the
// ops event must keep that body; otherwise the attempt is logged only as
// "xAI upstream returned status 403".
const grokUnknownForbiddenBody = `{"code":"permission-denied","error":"unrecognized provider refusal text"}`

func grokOpsDetailConfig(enabled bool, maxBytes int) *config.Config {
	return &config.Config{Gateway: config.GatewayConfig{
		LogUpstreamErrorBody:         enabled,
		LogUpstreamErrorBodyMaxBytes: maxBytes,
	}}
}

func grokForbiddenResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-forbidden"},
		},
		Body: io.NopCloser(strings.NewReader(grokUnknownForbiddenBody)),
	}
}

func lastGrokOpsEvent(t *testing.T, c *gin.Context) *OpsUpstreamErrorEvent {
	t.Helper()
	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.NotEmpty(t, events)
	return events[len(events)-1]
}

func TestForwardGrokResponsesRecordsUpstreamErrorDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		cfg        *config.Config
		wantDetail string
	}{
		{name: "enabled", cfg: grokOpsDetailConfig(true, 0), wantDetail: grokUnknownForbiddenBody},
		{name: "truncated", cfg: grokOpsDetailConfig(true, 16), wantDetail: grokUnknownForbiddenBody[:16]},
		{name: "disabled", cfg: grokOpsDetailConfig(false, 2048), wantDetail: ""},
		{name: "no config", cfg: nil, wantDetail: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			body := []byte(`{"model":"grok","input":"hi","stream":false}`)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

			account := &Account{
				ID:          4601,
				Name:        "grok-api-key",
				Platform:    PlatformGrok,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Credentials: map[string]any{"api_key": "token", "base_url": "https://api.x.ai/v1"},
			}
			repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
				accountsByID: map[int64]*Account{account.ID: account},
			}}
			svc := &OpenAIGatewayService{
				cfg:          tt.cfg,
				httpUpstream: &httpUpstreamRecorder{resp: grokForbiddenResponse()},
				accountRepo:  repo,
			}

			result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok", false, time.Now())
			require.Nil(t, result)
			require.Error(t, err)

			event := lastGrokOpsEvent(t, c)
			require.Equal(t, http.StatusForbidden, event.UpstreamStatusCode)
			require.Equal(t, "xai-forbidden", event.UpstreamRequestID)
			require.Equal(t, tt.wantDetail, event.Detail)
		})
	}
}

func TestForwardGrokChatViaResponsesRecordsUpstreamErrorDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"grok","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grokChatRawEndpoint, bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 7601, AllowLongContext: true})

	account := grokChatBridgeTestAccount(76)
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
		accountsByID: map[int64]*Account{account.ID: account},
	}}
	upstream := &httpUpstreamRecorder{resp: grokForbiddenResponse()}
	svc := &OpenAIGatewayService{
		cfg:               grokOpsDetailConfig(true, 2048),
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
	}

	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.Error(t, err)
	require.True(t, strings.HasSuffix(upstream.lastReq.URL.Path, grokChatResponsesEndpoint))

	event := lastGrokOpsEvent(t, c)
	require.Equal(t, http.StatusForbidden, event.UpstreamStatusCode)
	require.Equal(t, grokUnknownForbiddenBody, event.Detail)
}

func TestForwardGrokRawChatCompletionsRecordsUpstreamErrorDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		enabled    bool
		wantDetail string
	}{
		{name: "enabled", enabled: true, wantDetail: grokUnknownForbiddenBody},
		{name: "disabled", enabled: false, wantDetail: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"grok","messages":[{"role":"user","content":"hi"}],"stream":false}`)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			// API key accounts never use the Chat->Responses bridge, so this
			// exercises the raw Chat Completions forward.
			account := &Account{
				ID:          4602,
				Name:        "grok-api-key",
				Platform:    PlatformGrok,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Credentials: map[string]any{"api_key": "token", "base_url": "https://grok.example.test/v1"},
			}
			repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
				accountsByID: map[int64]*Account{account.ID: account},
			}}
			cfg := rawChatCompletionsTestConfig()
			cfg.Gateway.LogUpstreamErrorBody = tt.enabled
			cfg.Gateway.LogUpstreamErrorBodyMaxBytes = 2048
			upstream := &httpUpstreamRecorder{resp: grokForbiddenResponse()}
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, accountRepo: repo}

			_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
			require.Error(t, err)
			require.Equal(t, "https://grok.example.test/v1/chat/completions", upstream.lastReq.URL.String())

			event := lastGrokOpsEvent(t, c)
			require.Equal(t, http.StatusForbidden, event.UpstreamStatusCode)
			require.Equal(t, "xai-forbidden", event.UpstreamRequestID)
			require.Equal(t, tt.wantDetail, event.Detail)
		})
	}
}
