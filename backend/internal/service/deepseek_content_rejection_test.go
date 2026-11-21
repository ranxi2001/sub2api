package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const deepSeekInputRejectionBody = `{"error":{"message":"Input text data may contain inappropriate content.","type":"invalid_request_error","code":"invalid_request_error"}}`

func TestDeepSeekInputContentRejectionReturns403(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden} {
		for _, endpoint := range []string{"messages", "chat", "responses", "passthrough"} {
			t.Run(http.StatusText(status)+"/"+endpoint, func(t *testing.T) {
				// Broad account rules must not cool down or rotate a healthy key.
				account := &Account{ID: 1, Platform: PlatformDeepseek, Type: AccountTypeAPIKey,
					Credentials: map[string]any{"custom_error_codes_enabled": true, "custom_error_codes": []any{float64(status)}}}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, rateLimitService: &RateLimitService{}}
				c, rec := newOpenAIUpstreamErrorTestContext(t)
				resp := newOpenAIUpstreamErrorResponse(status, deepSeekInputRejectionBody)
				require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(account, status, "", []byte(deepSeekInputRejectionBody)))
				require.Nil(t, svc.failoverOpenAIUpstreamHTTPError(context.Background(), c, account, resp, []byte(deepSeekInputRejectionBody), DeepSeekInputContentRejectionMessage, "deepseek-flash"))
				var err error
				switch endpoint {
				case "messages":
					_, err = svc.handleAnthropicErrorResponse(resp, c, account)
				case "chat":
					_, err = svc.handleCompatErrorResponse(resp, c, account, writeChatCompletionsError)
				case "responses":
					_, err = svc.handleErrorResponse(context.Background(), resp, c, account, nil)
				case "passthrough":
					err = svc.handleErrorResponsePassthrough(context.Background(), resp, c, account, nil, []byte(deepSeekInputRejectionBody))
				}
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover))
				require.Equal(t, http.StatusForbidden, rec.Code)
				require.Equal(t, "permission_error", gjson.Get(rec.Body.String(), "error.type").String())
				require.Equal(t, DeepSeekInputContentRejectionMessage, gjson.Get(rec.Body.String(), "error.message").String())
				if endpoint == "messages" {
					require.Equal(t, "error", gjson.Get(rec.Body.String(), "type").String())
				}
				require.Equal(t, status, c.GetInt(OpsUpstreamStatusCodeKey))
				require.True(t, IsResponseCommitted(c))
				require.Nil(t, GetOpsCyberPolicy(c))
			})
		}
	}
}

func TestDeepSeekInputContentRejectionBoundaries(t *testing.T) {
	account := &Account{Platform: PlatformDeepseek}
	for _, body := range []string{
		`{"error":{"message":"Invalid API key"}}`,
		`{"error":{"message":"Output text data may contain inappropriate content."}}`,
		`{"error":{"message":"Input text data may contain inappropriate content. Please retry."}}`,
		`{"error":{"message":"Invalid parameter"},"input":{"message":"Input text data may contain inappropriate content."}}`,
	} {
		require.False(t, isDeepSeekInputContentResponse(account, 400, []byte(body)))
	}
	for _, status := range []int{200, 401, 402, 429, 500, 502, 503} {
		require.False(t, isDeepSeekInputContentResponse(account, status, []byte(deepSeekInputRejectionBody)))
	}
	account.Platform = PlatformOpenAI
	var gateway OpenAIGatewayService
	c, rec := newOpenAIUpstreamErrorTestContext(t)
	_, err := gateway.handleCompatErrorResponse(newOpenAIUpstreamErrorResponse(400, deepSeekInputRejectionBody), c, account, writeChatCompletionsError)
	require.Error(t, err)
	require.Equal(t, 400, rec.Code, "the same text from another platform keeps its existing status")
	require.False(t, isDeepSeekInputContentResponse(nil, 400, []byte(deepSeekInputRejectionBody)))
}

func TestNormalizeOpsDeepSeekContentRejection(t *testing.T) {
	for _, tc := range []struct {
		name, platform, phase string
		status, upstream      int
		want                  bool
	}{
		{"legacy", PlatformDeepseek, "upstream", 400, 400, true},
		{"normalized", PlatformDeepseek, "upstream", 403, 400, true},
		{"no upstream status", PlatformDeepseek, "upstream", 403, 0, true},
		{"recovered", PlatformDeepseek, "upstream", 200, 400, false},
		{"provider failure", PlatformDeepseek, "upstream", 502, 502, false},
		{"auth", PlatformDeepseek, "account_auth", 403, 400, false},
		{"other platform", PlatformOpenAI, "upstream", 400, 400, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := strings.ToUpper(DeepSeekInputContentRejectionMessage)
			entry := &OpsInsertErrorLogInput{Platform: tc.platform, StatusCode: tc.status, UpstreamStatusCode: &tc.upstream,
				ErrorOwner: "provider", ErrorPhase: tc.phase, ErrorType: "invalid_request_error", ErrorMessage: "Upstream request failed", UpstreamErrorMessage: &message}
			NormalizeOpsClientRejection(entry)
			require.Equal(t, tc.want, entry.IsBusinessLimited)
			require.Equal(t, tc.status, entry.StatusCode)
			require.Equal(t, tc.upstream, *entry.UpstreamStatusCode)
			require.Equal(t, message, *entry.UpstreamErrorMessage)
			if tc.want {
				require.Equal(t, "client", entry.ErrorOwner)
				require.Equal(t, "request", entry.ErrorPhase)
				require.Equal(t, "content_policy", entry.ErrorType)
			} else {
				require.Equal(t, "provider", entry.ErrorOwner)
			}
		})
	}
}
