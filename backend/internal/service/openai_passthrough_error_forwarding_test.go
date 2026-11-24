package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIGatewayService_APIKeyPassthrough_InvalidEncryptedContentPreservesStructured400(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const upstreamBody = `{
		"error": {
			"message": "The encrypted content gAAAAA_fake could not be verified. Reason: Encrypted content could not be decrypted or parsed.",
			"type": "invalid_request_error",
			"param": "input",
			"code": "invalid_encrypted_content",
			"internal_token": "sk-upstream-secret"
		},
		"debug": {"upstream": "secret-upstream.example"}
	}`

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusBadRequest,
			Header: http.Header{
				"Content-Type":  []string{"application/json"},
				"Server":        []string{"secret-upstream-proxy"},
				"Set-Cookie":    []string{"admin_token=secret"},
				"X-Admin-Debug": []string{"internal-route"},
			},
			Body: io.NopCloser(strings.NewReader(upstreamBody)),
		}},
	}
	account := &Account{
		ID:          201,
		Name:        "azure-passthrough",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://secret-upstream.example/openai/v1",
		},
		Extra:       map[string]any{"openai_passthrough": true},
		Status:      StatusActive,
		Schedulable: true,
	}

	_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.4","store":false,"input":[{"type":"reasoning","encrypted_content":"x"}]}`))

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	body := rec.Body.String()
	require.Equal(t, "invalid_request_error", gjson.Get(body, "error.type").String())
	require.Equal(t, "invalid_encrypted_content", gjson.Get(body, "error.code").String())
	require.Equal(t, "input", gjson.Get(body, "error.param").String())
	require.Equal(t, openAIEncryptedErrorRedacted, gjson.Get(body, "error.message").String())
	require.NotContains(t, body, "gAAAAA_fake")
	require.NotContains(t, body, "sk-upstream-secret")
	require.NotContains(t, body, "secret-upstream.example")
	require.False(t, gjson.Get(body, "debug").Exists())
	require.False(t, gjson.Get(body, "error.internal_token").Exists())
	require.Empty(t, rec.Header().Values("Server"))
	require.Empty(t, rec.Header().Values("Set-Cookie"))
	require.Empty(t, rec.Header().Values("X-Admin-Debug"))
}

func TestOpenAIGatewayService_APIKeyPassthrough_InvalidEncryptedContentRedactsUpstreamHostAndGuardsNon400(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("redacts configured base_url host inside allowlisted 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

		svc := &OpenAIGatewayService{
			cfg: &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
			httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"message":"https://secret-upstream.example/v1?key=secret123 rejected input for sk-test-secret-key","type":"invalid_request_error","code":"invalid_encrypted_content","param":"input"}}`,
				)),
			}},
		}
		account := &Account{
			ID: 202, Name: "azure-redact", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test-secret-key", "base_url": "https://secret-upstream.example/openai/v1"},
			Extra:       map[string]any{"openai_passthrough": true}, Status: StatusActive, Schedulable: true,
		}

		_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.4","input":"hi"}`))
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		body := rec.Body.String()
		require.Equal(t, "invalid_encrypted_content", gjson.Get(body, "error.code").String())
		require.NotContains(t, body, "secret-upstream.example")
		require.NotContains(t, body, "secret123")
		require.NotContains(t, body, "sk-test-secret-key")
		require.Contains(t, gjson.Get(body, "error.message").String(), "[upstream]")
		require.Contains(t, gjson.Get(body, "error.message").String(), "[redacted]")
	})

	t.Run("non-400 status with invalid_encrypted_content stays sanitized", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

		svc := &OpenAIGatewayService{
			cfg: &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
			httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"message":"secret-upstream.example denied","type":"invalid_request_error","code":"invalid_encrypted_content"}}`,
				)),
			}},
		}
		account := &Account{
			ID: 203, Name: "azure-403", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://secret-upstream.example"},
			Extra:       map[string]any{"openai_passthrough": true}, Status: StatusActive, Schedulable: true,
		}

		_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.4","input":"hi"}`))
		require.Error(t, err)
		require.Equal(t, http.StatusBadGateway, rec.Code)
		body := rec.Body.String()
		require.Equal(t, "upstream_error", gjson.Get(body, "error.type").String())
		require.Equal(t, "Upstream access denied", gjson.Get(body, "error.message").String())
		require.False(t, gjson.Get(body, "error.code").Exists())
		require.NotContains(t, body, "secret-upstream.example")
	})
}

func TestOpenAIGatewayService_APIKeyPassthrough_AppliesErrorPassthroughRules(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("passthrough_body=true preserves structured type/code/param and honors skip_monitoring", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

		ruleSvc := &ErrorPassthroughService{}
		ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{
			{
				ID:              1,
				Name:            "openai-400-passthrough",
				Enabled:         true,
				Priority:        1,
				ErrorCodes:      []int{http.StatusBadRequest},
				Keywords:        []string{"previous_response_not_found"},
				MatchMode:       model.MatchModeAny,
				Platforms:       []string{PlatformOpenAI},
				PassthroughCode: true,
				PassthroughBody: true,
				SkipMonitoring:  true,
			},
		})
		BindErrorPassthroughService(c, ruleSvc)

		svc := &OpenAIGatewayService{
			cfg: &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
			httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"Server":       []string{"internal-upstream"},
				},
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"message":"Previous response not found.","type":"invalid_request_error","code":"previous_response_not_found","param":"previous_response_id","internal_secret":"drop-me"},"debug":{"x":1}}`,
				)),
			}},
		}
		account := &Account{
			ID: 204, Name: "openai-pt-rule", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.example.test"},
			Extra:       map[string]any{"openai_passthrough": true}, Status: StatusActive, Schedulable: true,
		}

		_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.4","input":"hi"}`))
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, rec.Code)

		body := rec.Body.String()
		require.Equal(t, "invalid_request_error", gjson.Get(body, "error.type").String())
		require.Equal(t, "previous_response_not_found", gjson.Get(body, "error.code").String())
		require.Equal(t, "previous_response_id", gjson.Get(body, "error.param").String())
		require.Equal(t, "Previous response not found.", gjson.Get(body, "error.message").String())
		require.False(t, gjson.Get(body, "error.internal_secret").Exists())
		require.False(t, gjson.Get(body, "debug").Exists())
		require.Empty(t, rec.Header().Values("Server"))
		skipVal, ok := c.Get(OpsSkipPassthroughKey)
		require.True(t, ok)
		require.Equal(t, true, skipVal)
	})

	t.Run("custom message rule overrides invalid_encrypted_content default", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

		customStatus := http.StatusUnprocessableEntity
		customMsg := "Custom encrypted content error"
		ruleSvc := &ErrorPassthroughService{}
		ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{
			{
				ID:              2,
				Name:            "override-encrypted",
				Enabled:         true,
				Priority:        1,
				ErrorCodes:      []int{http.StatusBadRequest},
				Keywords:        []string{"invalid_encrypted_content"},
				MatchMode:       model.MatchModeAny,
				Platforms:       []string{PlatformOpenAI},
				PassthroughCode: false,
				ResponseCode:    &customStatus,
				PassthroughBody: false,
				CustomMessage:   &customMsg,
			},
		})
		BindErrorPassthroughService(c, ruleSvc)

		svc := &OpenAIGatewayService{
			cfg: &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
			httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"message":"Encrypted content could not be decrypted","type":"invalid_request_error","code":"invalid_encrypted_content"}}`,
				)),
			}},
		}
		account := &Account{
			ID: 205, Name: "openai-pt-custom", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test"},
			Extra:       map[string]any{"openai_passthrough": true}, Status: StatusActive, Schedulable: true,
		}

		_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.4","input":"hi"}`))
		require.Error(t, err)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		body := rec.Body.String()
		require.Equal(t, "upstream_error", gjson.Get(body, "error.type").String())
		require.Equal(t, customMsg, gjson.Get(body, "error.message").String())
		require.False(t, gjson.Get(body, "error.code").Exists())
	})
}

func TestOpenAIGatewayService_APIKeyPassthrough_ContextWindowPreservesCodeAndCompactSSEBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("context_length_exceeded preserves error.code", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

		svc := &OpenAIGatewayService{
			cfg: &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
			httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"message":"Your input exceeds the context window of this model.","type":"invalid_request_error","code":"context_length_exceeded","param":"input"}}`,
				)),
			}},
		}
		account := &Account{
			ID: 206, Name: "openai-pt-ctx", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test"},
			Extra:       map[string]any{"openai_passthrough": true}, Status: StatusActive, Schedulable: true,
		}

		_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.4","input":"hi"}`))
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		body := rec.Body.String()
		require.Equal(t, "invalid_request_error", gjson.Get(body, "error.type").String())
		require.Equal(t, "context_length_exceeded", gjson.Get(body, "error.code").String())
		require.Equal(t, "input", gjson.Get(body, "error.param").String())
		require.Contains(t, gjson.Get(body, "error.message").String(), "exceeds the context window")
	})

	t.Run("invalid_encrypted_content after compact keepalive emits response.failed SSE", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(nil))
		MarkOpenAICompactClientStream(c)
		stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
		defer stop()
		waitForKeepaliveBeats()

		svc := &OpenAIGatewayService{
			cfg: &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
			httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"message":"The encrypted content could not be verified.","type":"invalid_request_error","code":"invalid_encrypted_content"}}`,
				)),
			}},
		}
		account := &Account{
			ID: 207, Name: "openai-pt-compact", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test"},
			Extra:       map[string]any{"openai_passthrough": true}, Status: StatusActive, Schedulable: true,
		}

		_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.4","input":"hi"}`))
		require.Error(t, err)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Result().Header.Get("Content-Type"), "text/event-stream")
		events := parseCompactBridgeSSE(t, stripKeepaliveComments(rec.Body.String()))
		require.Len(t, events, 1)
		require.Equal(t, "response.failed", events[0][0])
		require.Equal(t, openAIEncryptedErrorRedacted, gjson.Get(events[0][1], "response.error.message").String())
		// 心跳已提交时 error.code 也必须保留，否则 compact 流上客户端仍无法识别该错误。
		require.Equal(t, "invalid_encrypted_content", gjson.Get(events[0][1], "response.error.code").String())
	})
}

func TestOpenAIGatewayService_APIKeyPassthrough_NestedErrorCodePreservesProtocolAndSanitization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	credentials := map[string]any{
		"api_key": "sk-test-secret-key", "base_url": "https://secret-upstream.example/v1",
		"access_token": "fake-access-secret", "refresh_token": "fake-refresh-secret",
	}
	for _, tc := range []struct {
		name       string
		innerCode  any
		wantCode   string
		classified bool
	}{
		{"encrypted", "invalid_encrypted_content", "invalid_encrypted_content", true},
		{"signature", "thinking_signature_invalid", "thinking_signature_invalid", true},
		// Rules exercise the same envelope with ordinary/sensitive codes. Only
		// the two exact protocol identifiers may bypass diagnostic redaction.
		{"ordinary", "inner_code", "inner_code", false},
		{"credential", "code:sk-test-secret-key", "code:[redacted]", false},
		{"host_and_query", "code:secret-upstream.example?key=query-secret", "code:[upstream]?key=***", false},
		{"encrypted_with_diagnostic", "invalid_encrypted_content sk-test-secret-key", openAIEncryptedErrorRedacted, false},
		{"signature_with_diagnostic", "thinking_signature_invalid sk-test-secret-key", openAIEncryptedErrorRedacted, false},
		{"oversized", strings.Repeat("x", openAIPassthroughClientErrorFieldMaxBytes+1), "", false},
		{"object", map[string]any{"internal_token": "sk-test-secret-key"}, "", false},
		{"array", []string{"sk-test-secret-key"}, "", false},
	} {
		for _, outer := range []struct {
			name    string
			present bool
			code    any
		}{
			{"missing", false, nil},
			{"null", true, nil},
			{"empty", true, ""},
			{"blank", true, " \t "},
		} {
			for _, mode := range []string{"json", "compact_committed"} {
				t.Run(tc.name+"/"+outer.name+"/"+mode, func(t *testing.T) {
					message := "Request rejected at secret-upstream.example?key=query-secret for sk-test-secret-key fake-access-secret fake-refresh-secret"
					wantMessage := "Request rejected at [upstream]?key=*** for [redacted] [redacted] [redacted]"
					if tc.classified {
						message = "The encrypted content gAAAAA_fake could not be verified. " + message
						wantMessage = openAIEncryptedErrorRedacted
					}
					inner, err := json.Marshal(map[string]any{"error": map[string]any{
						"code": tc.innerCode, "message": message, "internal_token": "sk-test-secret-key",
					}})
					require.NoError(t, err)
					errorFields := map[string]any{
						"message": string(inner), "type": "invalid_request_error",
						"param":          "input sk-test-secret-key secret-upstream.example?key=query-secret",
						"internal_token": "sk-test-secret-key",
					}
					if outer.present {
						errorFields["code"] = outer.code
					}
					body, err := json.Marshal(map[string]any{"error": errorFields, "debug": "sk-test-secret-key"})
					require.NoError(t, err)
					rec, c := newOpenAIPassthroughErrorTestContext()
					if mode == "compact_committed" {
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
						MarkOpenAICompactClientStream(c)
						stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
						defer stop()
						// Commit a real keepalive synchronously; no scheduler-dependent sleep.
						keepalive, ok := c.MustGet(openAICompactSSEKeepaliveKey).(*openAICompactSSEKeepalive)
						require.True(t, ok)
						require.True(t, keepalive.beat())
					}
					if !tc.classified {
						ruleSvc := &ErrorPassthroughService{}
						ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{{
							ID: 1, Name: "nested-error-body", Enabled: true, Priority: 1,
							ErrorCodes: []int{http.StatusBadRequest}, MatchMode: model.MatchModeAny,
							Platforms: []string{PlatformOpenAI}, PassthroughCode: true, PassthroughBody: true,
						}})
						BindErrorPassthroughService(c, ruleSvc)
					}
					// Forward runs the actual classifier and response writer, including
					// the committed compact bridge; do not test only the code helper.
					forwardOpenAIPassthroughErrorForTest(t, c, http.StatusBadRequest, string(body), credentials)
					payload, errorPath, wantCode := rec.Body.String(), "error", tc.wantCode
					if mode == "compact_committed" {
						require.Equal(t, http.StatusOK, rec.Code)
						require.Contains(t, rec.Result().Header.Get("Content-Type"), "text/event-stream")
						events := parseCompactBridgeSSE(t, stripKeepaliveComments(payload))
						require.Len(t, events, 1)
						require.Equal(t, "response.failed", events[0][0])
						payload, errorPath = events[0][1], "response.error"
						if wantCode == "" {
							wantCode = "upstream_error"
						}
					} else {
						require.Equal(t, http.StatusBadRequest, rec.Code)
						require.Equal(t, "invalid_request_error", gjson.Get(payload, "error.type").String())
						require.Equal(t, "input [redacted] [upstream]?key=***", gjson.Get(payload, "error.param").String())
					}
					if wantCode == "" {
						require.False(t, gjson.Get(payload, errorPath+".code").Exists())
					} else {
						require.Equal(t, wantCode, gjson.Get(payload, errorPath+".code").String())
					}
					require.Equal(t, wantMessage, gjson.Get(payload, errorPath+".message").String())
					for _, secret := range []string{"gAAAAA_fake", "sk-test-secret-key", "secret-upstream.example", "query-secret", "fake-access-secret", "fake-refresh-secret", "internal_token", "debug"} {
						require.NotContains(t, rec.Body.String(), secret)
					}
				})
			}
		}
	}
}

// forwardOpenAIPassthroughErrorForTest 让透传账号收到给定的上游错误响应，返回写给客户端的结果。
func forwardOpenAIPassthroughErrorForTest(t *testing.T, c *gin.Context, upstreamStatus int, upstreamBody string, credentials map[string]any) {
	t.Helper()
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: upstreamStatus,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(upstreamBody)),
		}},
	}
	account := &Account{
		ID: 301, Name: "openai-pt-hardening", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: credentials,
		Extra:       map[string]any{"openai_passthrough": true}, Status: StatusActive, Schedulable: true,
	}
	_, err := svc.Forward(c.Request.Context(), c, account, []byte(`{"model":"gpt-5.4","input":"hi"}`))
	require.Error(t, err)
}

func newOpenAIPassthroughErrorTestContext() (*httptest.ResponseRecorder, *gin.Context) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	return rec, c
}

// 大小写折叠会改变字节长度的字符（Ⱥ 变长、Kelvin 符号变短、非法 UTF-8 变长）
// 曾让主机名脱敏按错位下标切片：要么越界 panic，要么把主机名原样漏给客户端。
func TestReplaceCaseInsensitive_ByteLengthChangingCaseFolding(t *testing.T) {
	const host = "h.example"
	kelvin := "\u212a"
	cases := map[string]string{
		"ascii":                      "upstream h.example rejected",
		"mixed case host":            "upstream H.Example rejected",
		"grows when lowered":         "\u023a h.example",
		"grows, host in the middle":  strings.Repeat("\u023a", 10) + " h.example tail-padding-text-xxxxxxxx",
		"shrinks when lowered":       kelvin + " h.example",
		"shrinks, repeated":          strings.Repeat(kelvin, 5) + " h.example",
		"invalid utf8":               "\xff h.example",
		"invalid utf8, host in tail": strings.Repeat("\xff", 10) + " h.example tail-padding-text-xxxxxxxx",
		"multiple occurrences":       "\u023a h.example and " + kelvin + " H.EXAMPLE again h.example",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			var out string
			require.NotPanics(t, func() { out = replaceCaseInsensitive(input, host, "[upstream]") })
			require.NotContains(t, strings.ToLower(out), host)
			require.Contains(t, out, "[upstream]")
			// 主机名以外的字节必须原样保留。
			require.Equal(t,
				strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(input, "h.example", "[upstream]"), "H.Example", "[upstream]"), "H.EXAMPLE", "[upstream]"),
				out,
			)
		})
	}

	require.Equal(t, "no match here", replaceCaseInsensitive("no match here", host, "[upstream]"))
	require.Equal(t, "", replaceCaseInsensitive("", host, "[upstream]"))
	require.Equal(t, "text", replaceCaseInsensitive("text", "", "[upstream]"))
	// 正则元字符按字面量匹配，"." 不得匹配任意字符。
	require.Equal(t, "hxexample", replaceCaseInsensitive("hxexample", host, "[upstream]"))
	// target 无法编译时宁可丢弃整段文本。
	require.Equal(t, "", replaceCaseInsensitive("text \xff here", "\xff", "[upstream]"))
}

func TestOpenAIGatewayService_APIKeyPassthrough_StructuredErrorHardening(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("case-folding characters next to the upstream host neither panic nor leak it", func(t *testing.T) {
		for name, prefix := range map[string]string{
			"grows":        "\u023a",
			"shrinks":      strings.Repeat("\u212a", 5),
			"invalid utf8": "\xff",
		} {
			t.Run(name, func(t *testing.T) {
				rec, c := newOpenAIPassthroughErrorTestContext()
				upstreamBody := `{"error":{"message":"` + prefix + ` h.example","type":"invalid_request_error","code":"context_length_exceeded","param":"input"}}`
				require.NotPanics(t, func() {
					forwardOpenAIPassthroughErrorForTest(t, c, http.StatusBadRequest, upstreamBody,
						map[string]any{"api_key": "sk-test", "base_url": "https://h.example/v1"})
				})
				require.Equal(t, http.StatusBadRequest, rec.Code)
				body := rec.Body.String()
				require.NotContains(t, strings.ToLower(body), "h.example")
				require.Contains(t, gjson.Get(body, "error.message").String(), "[upstream]")
				require.Equal(t, "context_length_exceeded", gjson.Get(body, "error.code").String())
			})
		}
	})

	t.Run("non-scalar type and param are dropped instead of forwarded as raw JSON", func(t *testing.T) {
		rec, c := newOpenAIPassthroughErrorTestContext()
		forwardOpenAIPassthroughErrorForTest(t, c, http.StatusBadRequest,
			`{"error":{"message":"The encrypted content could not be verified.","type":{"internal_url":"http://10.0.0.1/admin"},"code":"invalid_encrypted_content","param":["internal_token","tok-abcdef"]}}`,
			map[string]any{"api_key": "sk-test"})
		require.Equal(t, http.StatusBadRequest, rec.Code)
		body := rec.Body.String()
		require.Equal(t, openAIUpstreamClientErrorFallbackType, gjson.Get(body, "error.type").String())
		require.Equal(t, "invalid_encrypted_content", gjson.Get(body, "error.code").String())
		require.False(t, gjson.Get(body, "error.param").Exists())
		require.NotContains(t, body, "10.0.0.1")
		require.NotContains(t, body, "internal_token")
		require.NotContains(t, body, "tok-abcdef")
	})

	t.Run("non-scalar code is not forwarded", func(t *testing.T) {
		rec, c := newOpenAIPassthroughErrorTestContext()
		forwardOpenAIPassthroughErrorForTest(t, c, http.StatusBadRequest,
			`{"error":{"message":"Your input exceeds the context window of this model.","type":"invalid_request_error","code":{"internal_token":"tok-abcdef"}}}`,
			map[string]any{"api_key": "sk-test"})
		require.Equal(t, http.StatusBadRequest, rec.Code)
		body := rec.Body.String()
		require.Equal(t, "context_length_exceeded", gjson.Get(body, "error.code").String())
		require.NotContains(t, body, "tok-abcdef")
		require.Contains(t, gjson.Get(body, "error.message").String(), "exceeds the context window")
	})

	t.Run("oversized fields are dropped", func(t *testing.T) {
		rec, c := newOpenAIPassthroughErrorTestContext()
		oversized := strings.Repeat("p", openAIPassthroughClientErrorFieldMaxBytes+1)
		forwardOpenAIPassthroughErrorForTest(t, c, http.StatusBadRequest,
			`{"error":{"message":"The encrypted content could not be verified.","type":"`+oversized+`","code":"invalid_encrypted_content","param":"`+oversized+`"}}`,
			map[string]any{"api_key": "sk-test"})
		require.Equal(t, http.StatusBadRequest, rec.Code)
		body := rec.Body.String()
		require.Equal(t, openAIUpstreamClientErrorFallbackType, gjson.Get(body, "error.type").String())
		require.False(t, gjson.Get(body, "error.param").Exists())
		require.NotContains(t, body, oversized)
	})

	t.Run("numeric code is forwarded as a string", func(t *testing.T) {
		require.Equal(t, "1234", openAIPassthroughClientErrorCode(nil, []byte(`{"error":{"code":1234}}`)))
		require.Equal(t, "", openAIPassthroughClientErrorCode(nil, []byte(`{"error":{"code":null}}`)))
		require.Equal(t, "", openAIPassthroughClientErrorCode(nil, []byte(`{"error":{"code":true}}`)))
		require.Equal(t, "inner_code", openAIPassthroughClientErrorCode(nil, []byte(`{"error":{"message":"{\"error\":{\"code\":\"inner_code\"}}"}}`)))
		require.Equal(t, "", openAIPassthroughClientErrorCode(nil, []byte(`{"error":{"message":"{\"error\":{\"code\":{\"k\":\"v\"}}}"}}`)))
		// error.code 为空白字符串时与分类逻辑一致，回退到 message 内嵌 JSON 的 code。
		require.Equal(t, "inner_code", openAIPassthroughClientErrorCode(nil, []byte(`{"error":{"code":"","message":"{\"error\":{\"code\":\"inner_code\"}}"}}`)))
		require.Equal(t, "inner_code", openAIPassthroughClientErrorCode(nil, []byte(`{"error":{"code":"  ","message":"{\"error\":{\"code\":\"inner_code\"}}"}}`)))
		require.Equal(t, "inner_code", openAIPassthroughClientErrorCode(nil, []byte(`{"error":{"code":null,"message":"{\"error\":{\"code\":\"inner_code\"}}"}}`)))
		// error.code 是非标量时不回退。
		require.Equal(t, "", openAIPassthroughClientErrorCode(nil, []byte(`{"error":{"code":{"k":"v"},"message":"{\"error\":{\"code\":\"inner_code\"}}"}}`)))
	})

	t.Run("ordinary 400 that merely mentions invalid_encrypted_content stays sanitized", func(t *testing.T) {
		rec, c := newOpenAIPassthroughErrorTestContext()
		forwardOpenAIPassthroughErrorForTest(t, c, http.StatusBadRequest,
			`{"error":{"message":"echoed user text: invalid_encrypted_content at secret-upstream.example","type":"invalid_request_error","code":"invalid_value","param":"input"}}`,
			map[string]any{"api_key": "sk-test", "base_url": "https://secret-upstream.example/v1"})
		require.Equal(t, http.StatusBadRequest, rec.Code)
		body := rec.Body.String()
		require.Equal(t, "upstream_error", gjson.Get(body, "error.type").String())
		require.Equal(t, "Upstream request failed", gjson.Get(body, "error.message").String())
		require.False(t, gjson.Get(body, "error.code").Exists())
		require.False(t, gjson.Get(body, "error.param").Exists())
		require.NotContains(t, body, "secret-upstream.example")
	})
}

func TestOpenAIGatewayService_APIKeyPassthrough_RuleWithoutCustomMessageSanitizesUpstreamMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	bindRule := func(c *gin.Context, customMessage *string) {
		ruleSvc := &ErrorPassthroughService{}
		ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{{
			ID:              1,
			Name:            "openai-400-rule",
			Enabled:         true,
			Priority:        1,
			ErrorCodes:      []int{http.StatusBadRequest},
			MatchMode:       model.MatchModeAny,
			Platforms:       []string{PlatformOpenAI},
			PassthroughCode: true,
			PassthroughBody: false,
			CustomMessage:   customMessage,
		}})
		BindErrorPassthroughService(c, ruleSvc)
	}
	const upstreamBody = `{"error":{"message":"https://secret-upstream.example/v1?key=secret123 rejected request for sk-test-secret-key","type":"invalid_request_error","code":"invalid_value"}}`
	credentials := map[string]any{"api_key": "sk-test-secret-key", "base_url": "https://secret-upstream.example/v1"}

	t.Run("upstream message is redacted when the rule has no custom message", func(t *testing.T) {
		rec, c := newOpenAIPassthroughErrorTestContext()
		bindRule(c, nil)
		forwardOpenAIPassthroughErrorForTest(t, c, http.StatusBadRequest, upstreamBody, credentials)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		body := rec.Body.String()
		require.Equal(t, "upstream_error", gjson.Get(body, "error.type").String())
		require.False(t, gjson.Get(body, "error.code").Exists())
		require.NotContains(t, body, "secret-upstream.example")
		require.NotContains(t, body, "secret123")
		require.NotContains(t, body, "sk-test-secret-key")
		require.Contains(t, gjson.Get(body, "error.message").String(), "rejected request")
	})

	t.Run("administrator custom message is written verbatim", func(t *testing.T) {
		rec, c := newOpenAIPassthroughErrorTestContext()
		custom := "Contact support at secret-upstream.example"
		bindRule(c, &custom)
		forwardOpenAIPassthroughErrorForTest(t, c, http.StatusBadRequest, upstreamBody, credentials)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, custom, gjson.Get(rec.Body.String(), "error.message").String())
	})
}

// Codex 把 server_is_overloaded / slow_down 判为致命错误并终止会话，写进
// response.failed 前必须改写为可重试的错误码；其它上游 code 原样保留。
func TestOpenAIGatewayService_APIKeyPassthrough_CompactFailedRewritesCapacityShedCode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for upstreamCode, wantCode := range map[string]string{
		"server_is_overloaded": openAICapacityShedRetryableClientCode,
		"slow_down":            openAICapacityShedRetryableClientCode,
		"SERVER_IS_OVERLOADED": openAICapacityShedRetryableClientCode,
		"rate_limit_exceeded":  "rate_limit_exceeded",
	} {
		t.Run(upstreamCode, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(nil))
			MarkOpenAICompactClientStream(c)
			stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
			defer stop()
			waitForKeepaliveBeats()

			ruleSvc := &ErrorPassthroughService{}
			ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{{
				ID: 1, Name: "openai-400-body", Enabled: true, Priority: 1,
				ErrorCodes: []int{http.StatusBadRequest}, MatchMode: model.MatchModeAny,
				Platforms: []string{PlatformOpenAI}, PassthroughCode: true, PassthroughBody: true,
			}})
			BindErrorPassthroughService(c, ruleSvc)

			forwardOpenAIPassthroughErrorForTest(t, c, http.StatusBadRequest,
				`{"error":{"message":"Selected model is at capacity.","type":"server_error","code":"`+upstreamCode+`"}}`,
				map[string]any{"api_key": "sk-test"})

			require.Equal(t, http.StatusOK, rec.Code)
			events := parseCompactBridgeSSE(t, stripKeepaliveComments(rec.Body.String()))
			require.Len(t, events, 1)
			require.Equal(t, "response.failed", events[0][0])
			require.Equal(t, wantCode, gjson.Get(events[0][1], "response.error.code").String())
			require.Contains(t, gjson.Get(events[0][1], "response.error.message").String(), "at capacity")
		})
	}
}

// Exercise the same client oracle through Forward, including the committed
// compact bridge. All header values and identities below are synthetic.
func TestOpenAIGatewayService_APIKeyPassthrough_HeaderOverrideSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, branch := range []string{"default", "rule_body", "rule_message"} {
		for _, mode := range []string{"json", "compact_committed"} {
			for _, tc := range []struct {
				name, template, echo, want string
			}{
				{"static", "relay-secret-123", "relay-secret-123", "[redacted]"},
				{"identity_template", "relay-prefix-123{{user_id}}relay-suffix-456", "relay-prefix-12342relay-suffix-456", "[redacted]42[redacted]"},
				{"overlapping_literals", "synthetic-primary-key-suffix", "synthetic-primary-key-suffix", "[redacted]"},
				{"short_control", "x{{user_id}}", "ordinary text: x", "ordinary text: x"},
				{"separator_control", "------{{user_id}}", "ordinary ------ text", "ordinary ------ text"},
				{"identity_only_control", "{{user_id}}", "ordinary text", "ordinary text"},
			} {
				t.Run(branch+"/"+mode+"/"+tc.name, func(t *testing.T) {
					rec, c := newOpenAIPassthroughErrorTestContext()
					if mode == "compact_committed" {
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
						MarkOpenAICompactClientStream(c)
						stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
						defer stop()
						keepalive, ok := c.MustGet(openAICompactSSEKeepaliveKey).(*openAICompactSSEKeepalive)
						require.True(t, ok)
						require.True(t, keepalive.beat())
					}
					c.Request = c.Request.WithContext(WithHeaderOverrideRequestScope(c.Request.Context(), 42, 84, nil))
					code, wantCode := "invalid_encrypted_content", "invalid_encrypted_content"
					if branch != "default" {
						code, wantCode = "code:"+tc.echo, "code:"+tc.want
						rules := &ErrorPassthroughService{}
						rules.setLocalCache([]*model.ErrorPassthroughRule{{
							ID: 1, Name: "synthetic-header-error", Enabled: true, Priority: 1,
							ErrorCodes: []int{400}, MatchMode: model.MatchModeAny,
							Platforms: []string{PlatformOpenAI}, PassthroughCode: true,
							PassthroughBody: branch == "rule_body",
						}})
						BindErrorPassthroughService(c, rules)
					}
					body, err := json.Marshal(map[string]any{"error": map[string]any{
						"code": code, "message": "Rejected " + tc.echo,
						"type": "type:" + tc.echo, "param": "param:" + tc.echo,
					}})
					require.NoError(t, err)
					forwardOpenAIPassthroughErrorForTest(t, c, 400, string(body), map[string]any{
						"api_key": "synthetic-primary-key", "header_override_enabled": true,
						"header_overrides": map[string]any{"x-relay-auth": tc.template},
					})
					payload, path := rec.Body.String(), "error"
					if mode == "compact_committed" {
						require.Equal(t, 200, rec.Code)
						events := parseCompactBridgeSSE(t, stripKeepaliveComments(rec.Body.String()))
						require.Len(t, events, 1)
						require.Equal(t, "response.failed", events[0][0])
						payload, path = events[0][1], "response.error"
					} else {
						require.Equal(t, 400, rec.Code)
						if branch != "rule_message" {
							require.Equal(t, "type:"+tc.want, gjson.Get(payload, "error.type").String())
							require.Equal(t, "param:"+tc.want, gjson.Get(payload, "error.param").String())
						}
					}
					require.Equal(t, "Rejected "+tc.want, gjson.Get(payload, path+".message").String())
					if branch != "rule_message" {
						require.Equal(t, wantCode, gjson.Get(payload, path+".code").String())
					}
					for _, secret := range []string{"relay-secret-123", "relay-prefix-123", "relay-suffix-456", "synthetic-primary-key"} {
						require.NotContains(t, rec.Body.String(), secret)
					}
				})
			}
		}
	}
}

func TestOpenAIGatewayService_APIKeyPassthrough_ProtocolCodeWhitespaceAndSecretCollision(t *testing.T) {
	for _, code := range []string{"invalid_encrypted_content", "thinking_signature_invalid"} {
		for _, nested := range []bool{false, true} {
			for _, secretCollision := range []bool{false, true} {
				for _, compact := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/nested=%t/secret=%t/compact=%t", code, nested, secretCollision, compact), func(t *testing.T) {
						rec, c := newOpenAIPassthroughErrorTestContext()
						if compact {
							MarkOpenAICompactClientStream(c)
							stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
							defer stop()
							keepalive, ok := c.MustGet(openAICompactSSEKeepaliveKey).(*openAICompactSSEKeepalive)
							require.True(t, ok)
							require.True(t, keepalive.beat())
						}
						fields := map[string]any{"code": " \t" + code + " ", "message": "The encrypted content gAAAAA_fake could not be verified."}
						if nested {
							inner, err := json.Marshal(map[string]any{"error": fields})
							require.NoError(t, err)
							fields = map[string]any{"message": string(inner)}
						}
						body, err := json.Marshal(map[string]any{"error": fields})
						require.NoError(t, err)
						credentials := map[string]any{"api_key": "synthetic-primary-key"}
						want := code
						if secretCollision {
							credentials["header_override_enabled"] = true
							credentials["header_overrides"] = map[string]any{"x-relay-auth": code}
							want = "[redacted]"
						}
						forwardOpenAIPassthroughErrorForTest(t, c, 400, string(body), credentials)
						payload, path := rec.Body.String(), "error"
						if compact {
							events := parseCompactBridgeSSE(t, stripKeepaliveComments(payload))
							require.Len(t, events, 1)
							payload, path = events[0][1], "response.error"
						}
						require.Equal(t, want, gjson.Get(payload, path+".code").String())
						require.Equal(t, openAIEncryptedErrorRedacted, gjson.Get(payload, path+".message").String())
						require.NotContains(t, rec.Body.String(), "gAAAAA_fake")
					})
				}
			}
		}
	}
}
