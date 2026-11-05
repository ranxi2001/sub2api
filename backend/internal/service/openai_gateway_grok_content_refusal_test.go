//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// xAI answers a prompt its chat model refuses with 403 permission-denied and
// the refusal sentence as the error string. Retrying another account returns
// the same sentence, so it must be handled as a content-policy rejection.
const grokChatRefusalBody = `{"code":"permission-denied","error":"I'm sorry, I can't help with that request."}`

func grokChatRefusalResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-refusal"},
		},
		Body: io.NopCloser(strings.NewReader(grokChatRefusalBody)),
	}
}

func grokRefusalPassthroughRule() *model.ErrorPassthroughRule {
	respCode := http.StatusBadRequest
	message := "请求内容被安全策略拒绝"
	return &model.ErrorPassthroughRule{
		ID:             19,
		Name:           "grok-chat-refusal",
		Enabled:        true,
		Priority:       2,
		ErrorCodes:     []int{http.StatusForbidden},
		Keywords:       []string{"help with that request"},
		MatchMode:      model.MatchModeAll,
		ResponseCode:   &respCode,
		CustomMessage:  &message,
		SkipMonitoring: true,
	}
}

func grokRefusalErrorField(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	return errField
}

func TestGrokChatRefusalIsContentPolicyRejection(t *testing.T) {
	curly := strings.ReplaceAll(grokChatRefusalBody, "'", "’")

	require.True(t, isGrokContentPolicyRejection(http.StatusForbidden, []byte(grokChatRefusalBody)))
	require.True(t, isGrokContentPolicyRejection(http.StatusForbidden, []byte(curly)))
	require.False(t, isGrokUnknownForbidden(http.StatusForbidden, []byte(grokChatRefusalBody)))
	require.False(t, (&OpenAIGatewayService{}).shouldFailoverGrokUpstreamError(http.StatusForbidden, []byte(grokChatRefusalBody)))

	// Only a 403 is a refusal; account-access wording still wins.
	require.False(t, isGrokContentPolicyRejection(http.StatusBadRequest, []byte(grokChatRefusalBody)))
	require.False(t, isGrokContentPolicyRejection(http.StatusForbidden,
		[]byte(`{"code":"permission-denied","error":"Account suspended. I'm sorry, I can't help with that request."}`)))
}

func TestForwardGrokResponsesChatRefusalStaysOnAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		rule        *model.ErrorPassthroughRule
		wantStatus  int
		wantType    string
		wantMessage string
	}{
		{
			name:        "default response",
			wantStatus:  http.StatusForbidden,
			wantType:    "invalid_request_error",
			wantMessage: "Request blocked by upstream content policy",
		},
		{
			name:        "passthrough rule localizes",
			rule:        grokRefusalPassthroughRule(),
			wantStatus:  http.StatusBadRequest,
			wantType:    "upstream_error",
			wantMessage: "请求内容被安全策略拒绝",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			body := []byte(`{"model":"grok","input":"hi","stream":false}`)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			if tt.rule != nil {
				ruleSvc := &ErrorPassthroughService{}
				ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{tt.rule})
				BindErrorPassthroughService(c, ruleSvc)
			}

			account := &Account{
				ID:          4611,
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
			upstream := &httpUpstreamRecorder{resp: grokChatRefusalResponse()}
			svc := &OpenAIGatewayService{httpUpstream: upstream, accountRepo: repo}

			result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok", false, time.Now())
			require.Nil(t, result)
			require.Error(t, err)
			require.True(t, isGrokRequestScopedFailure(err))
			var failoverErr *UpstreamFailoverError
			require.NotErrorAs(t, err, &failoverErr)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, StatusActive, account.Status)
			require.True(t, account.Schedulable)

			require.Equal(t, tt.wantStatus, rec.Code)
			errField := grokRefusalErrorField(t, rec)
			require.Equal(t, tt.wantType, errField["type"])
			require.Equal(t, tt.wantMessage, errField["message"])
			_, skipped := c.Get(OpsSkipPassthroughKey)
			require.Equal(t, tt.rule != nil, skipped)
		})
	}
}

func TestForwardGrokChatCompletionsRefusalAppliesPassthroughRule(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name    string
		account func() *Account
		svc     func(repo *grokQuotaAccountRepo, upstream *httpUpstreamRecorder) *OpenAIGatewayService
	}{
		{
			// API key accounts use the raw Chat Completions forward.
			name: "raw chat",
			account: func() *Account {
				return &Account{
					ID:          4612,
					Name:        "grok-api-key",
					Platform:    PlatformGrok,
					Type:        AccountTypeAPIKey,
					Status:      StatusActive,
					Schedulable: true,
					Concurrency: 1,
					Credentials: map[string]any{"api_key": "token", "base_url": "https://grok.example.test/v1"},
				}
			},
			svc: func(repo *grokQuotaAccountRepo, upstream *httpUpstreamRecorder) *OpenAIGatewayService {
				return &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream, accountRepo: repo}
			},
		},
		{
			// OAuth accounts bridge eligible chat requests to Responses.
			name:    "chat to responses bridge",
			account: func() *Account { return grokChatBridgeTestAccount(77) },
			svc: func(repo *grokQuotaAccountRepo, upstream *httpUpstreamRecorder) *OpenAIGatewayService {
				return &OpenAIGatewayService{
					httpUpstream:      upstream,
					grokTokenProvider: NewGrokTokenProvider(repo, nil),
					accountRepo:       repo,
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"grok","messages":[{"role":"user","content":"hi"}],"stream":false}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set("api_key", &APIKey{ID: 7701, AllowLongContext: true})
			ruleSvc := &ErrorPassthroughService{}
			ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{grokRefusalPassthroughRule()})
			BindErrorPassthroughService(c, ruleSvc)

			account := tt.account()
			repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
				accountsByID: map[int64]*Account{account.ID: account},
			}}
			upstream := &httpUpstreamRecorder{resp: grokChatRefusalResponse()}
			svc := tt.svc(repo, upstream)

			_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
			require.Error(t, err)
			require.True(t, isGrokRequestScopedFailure(err))
			var failoverErr *UpstreamFailoverError
			require.NotErrorAs(t, err, &failoverErr)
			require.Len(t, upstream.requests, 1)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			errField := grokRefusalErrorField(t, rec)
			require.Equal(t, "请求内容被安全策略拒绝", errField["message"])
		})
	}
}
