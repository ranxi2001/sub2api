//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newMimicClaudeCodeAccountForTest(id int64, extra map[string]any) *Account {
	merged := map[string]any{
		"anthropic_mimic_claude_code":               true,
		"anthropic_mimic_claude_code_deduct_tokens": true,
	}
	for k, v := range extra {
		merged[k] = v
	}
	return &Account{
		ID:          id,
		Name:        "claude-code-only-relay",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "relay-key",
			"base_url": "https://relay.example.com",
		},
		Extra:       merged,
		Status:      StatusActive,
		Schedulable: true,
	}
}

// requireClaudeCodeOnlyGroupAccepts 用 sub2api 自己的"仅允许 Claude Code"判定检查发往上游的请求。
func requireClaudeCodeOnlyGroupAccepts(t *testing.T, req *http.Request, wireBody []byte) {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(wireBody, &body))
	// 出站头按线上大小写原样写（如 user-agent），上游 net/http 解析时会规范化，这里照做。
	probe := httptest.NewRequest(http.MethodPost, req.URL.Path, nil)
	for key, values := range req.Header {
		for _, value := range values {
			probe.Header.Add(key, value)
		}
	}
	require.True(t, NewClaudeCodeValidator().Validate(probe, body), "上游的 Claude Code 判定应放行")
}

func newPlainClientContext(body []byte) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("User-Agent", "python-httpx/0.28.1")
	req.Header.Set("anthropic-beta", "interleaved-thinking-2025-05-14")
	c.Request = req
	c.Set("api_key", &APIKey{ID: 9001})
	return c
}

func TestAccount_IsAnthropicMimicClaudeCodeEnabled(t *testing.T) {
	require.True(t, newMimicClaudeCodeAccountForTest(1, nil).IsAnthropicMimicClaudeCodeEnabled())
	require.False(t, newMimicClaudeCodeAccountForTest(1, map[string]any{"anthropic_mimic_claude_code": false}).IsAnthropicMimicClaudeCodeEnabled())
	require.False(t, newMimicClaudeCodeAccountForTest(1, map[string]any{"anthropic_mimic_claude_code": "true"}).IsAnthropicMimicClaudeCodeEnabled())
	require.False(t, newMimicClaudeCodeAccountForTest(1, map[string]any{"anthropic_strip_claude_code_identity": true}).IsAnthropicMimicClaudeCodeEnabled(),
		"与抹除身份同时开启时以抹除为准")

	oauth := newMimicClaudeCodeAccountForTest(1, nil)
	oauth.Type = AccountTypeOAuth
	require.False(t, oauth.IsAnthropicMimicClaudeCodeEnabled())
	openai := newMimicClaudeCodeAccountForTest(1, nil)
	openai.Platform = PlatformOpenAI
	require.False(t, openai.IsAnthropicMimicClaudeCodeEnabled())
	require.False(t, (*Account)(nil).IsAnthropicMimicClaudeCodeEnabled())
}

func TestAccount_IsAnthropicMimicClaudeCodeDeductTokensEnabled(t *testing.T) {
	account := newMimicClaudeCodeAccountForTest(1, nil)
	require.True(t, account.IsAnthropicMimicClaudeCodeDeductTokensEnabled())
	delete(account.Extra, "anthropic_mimic_claude_code_deduct_tokens")
	require.False(t, account.IsAnthropicMimicClaudeCodeDeductTokensEnabled(), "default keeps normal billing")
	account.Extra["anthropic_mimic_claude_code_deduct_tokens"] = "true"
	require.False(t, account.IsAnthropicMimicClaudeCodeDeductTokensEnabled())
	account.Extra["anthropic_mimic_claude_code_deduct_tokens"] = true
	account.Extra["anthropic_mimic_claude_code"] = false
	require.False(t, account.IsAnthropicMimicClaudeCodeDeductTokensEnabled())
	account.Extra["anthropic_mimic_claude_code"] = true
	account.Extra["anthropic_strip_claude_code_identity"] = true
	require.False(t, account.IsAnthropicMimicClaudeCodeDeductTokensEnabled())
	require.False(t, (*Account)(nil).IsAnthropicMimicClaudeCodeDeductTokensEnabled())
}

func TestClaudeCodeIdentityExtraTokens(t *testing.T) {
	// 数值对应 2026-10-31 用上游 count_tokens 实测的结果。
	require.Equal(t, 24, claudeCodeIdentityExtraTokens("claude-opus-5-5", false))
	require.Equal(t, 23, claudeCodeIdentityExtraTokens("claude-opus-5-5", true))
	require.Equal(t, 23, claudeCodeIdentityExtraTokens("claude-opus-4-7", true))
	require.Equal(t, 23, claudeCodeIdentityExtraTokens("claude-fable-5-1", true))
	require.Equal(t, 15, claudeCodeIdentityExtraTokens("claude-sonnet-4-6", false))
	require.Equal(t, 14, claudeCodeIdentityExtraTokens("claude-haiku-4-5-20251001", true))
	require.Equal(t, 14, claudeCodeIdentityExtraTokens("anthropic/claude-sonnet-4.5", true))
	require.Equal(t, 24, claudeCodeIdentityExtraTokens("some-future-model", false))
}

func TestMimicAnthropicClaudeCodeBody(t *testing.T) {
	account := newMimicClaudeCodeAccountForTest(77, nil)
	ua := "claude-cli/2.1.302 (external, cli)"

	t.Run("no system gets identity block and a valid metadata user_id", func(t *testing.T) {
		raw := []byte(`{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"ping"}]}`)
		got, injection := mimicAnthropicClaudeCodeBody(raw, account, 9001, ua, "claude-opus-5-5")

		blocks := gjson.GetBytes(got, "system").Array()
		require.Len(t, blocks, 1)
		require.Equal(t, claudeCodeSystemPrompt, blocks[0].Get("text").String())
		require.False(t, blocks[0].Get("cache_control").Exists(), "补的身份句不带缓存断点")
		require.NotNil(t, ParseMetadataUserID(gjson.GetBytes(got, "metadata.user_id").String()))
		require.Equal(t, claudeCodeMimicInjection{AccountID: 77, Tokens: 24}, injection)
		require.Equal(t, "ping", gjson.GetBytes(got, "messages.0.content").String())
	})

	t.Run("string system becomes identity plus the original text", func(t *testing.T) {
		raw := []byte(`{"model":"claude-sonnet-4-6","system":"You are a helpful translator.","messages":[{"role":"user","content":"hi"}]}`)
		got, injection := mimicAnthropicClaudeCodeBody(raw, account, 9001, ua, "claude-sonnet-4-6")

		blocks := gjson.GetBytes(got, "system").Array()
		require.Len(t, blocks, 2)
		require.Equal(t, claudeCodeSystemPrompt, blocks[0].Get("text").String())
		require.Equal(t, "You are a helpful translator.", blocks[1].Get("text").String())
		require.Equal(t, 14, injection.Tokens)
		require.False(t, injection.Cached)
	})

	t.Run("cache breakpoint after the identity decides the bucket and ttl", func(t *testing.T) {
		raw := []byte(`{"model":"claude-opus-5-5","tools":[{"name":"t","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],"system":[{"type":"text","text":"Long rules.","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"hi"}]}`)
		got, injection := mimicAnthropicClaudeCodeBody(raw, account, 9001, ua, "claude-opus-5-5")

		require.Equal(t, claudeCodeMimicInjection{AccountID: 77, Tokens: 23, Cached: true, CacheTTL1h: true}, injection)
		require.Equal(t, "1h", gjson.GetBytes(got, "system.1.cache_control.ttl").String(), "客户端的缓存断点原样保留")

		messagesOnly := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`)
		_, injection = mimicAnthropicClaudeCodeBody(messagesOnly, account, 9001, ua, "claude-opus-5-5")
		require.True(t, injection.Cached)
		require.False(t, injection.CacheTTL1h)

		toolsOnly := []byte(`{"model":"claude-opus-5-5","tools":[{"name":"t","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`)
		_, injection = mimicAnthropicClaudeCodeBody(toolsOnly, account, 9001, ua, "claude-opus-5-5")
		require.False(t, injection.Cached, "tools 在 system 前面，它的断点盖不到身份句")
	})

	t.Run("real Claude Code body is left untouched", func(t *testing.T) {
		raw := []byte(`{"model":"claude-opus-5-5","system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.302.abc; cc_entrypoint=cli;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude.","cache_control":{"type":"ephemeral"}}],"metadata":{"user_id":"user_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef_account__session_11111111-2222-3333-4444-555555555555"},"messages":[{"role":"user","content":"hi"}]}`)
		got, injection := mimicAnthropicClaudeCodeBody(raw, account, 9001, ua, "claude-opus-5-5")
		require.Equal(t, raw, got)
		require.Zero(t, injection.Tokens)
	})

	t.Run("invalid user_id is replaced while other metadata keys stay", func(t *testing.T) {
		raw := []byte(`{"model":"claude-opus-5-5","metadata":{"user_id":"user-123","trace":"keep"},"messages":[{"role":"user","content":"hi"}]}`)
		got, _ := mimicAnthropicClaudeCodeBody(raw, account, 9001, ua, "claude-opus-5-5")
		require.NotNil(t, ParseMetadataUserID(gjson.GetBytes(got, "metadata.user_id").String()))
		require.Equal(t, "keep", gjson.GetBytes(got, "metadata.trace").String())
	})

	t.Run("session id is stable per conversation and differs per api key", func(t *testing.T) {
		first := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"same opening"}]}`)
		later := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"same opening"},{"role":"assistant","content":"ok"},{"role":"user","content":"next"}]}`)
		sessionOf := func(body []byte, apiKeyID int64) string {
			got, _ := mimicAnthropicClaudeCodeBody(body, account, apiKeyID, ua, "claude-opus-5-5")
			parsed := ParseMetadataUserID(gjson.GetBytes(got, "metadata.user_id").String())
			require.NotNil(t, parsed)
			return parsed.SessionID
		}
		require.Equal(t, sessionOf(first, 9001), sessionOf(later, 9001))
		require.NotEqual(t, sessionOf(first, 9001), sessionOf(first, 9002))
	})

	t.Run("non-array object system is not touched", func(t *testing.T) {
		raw := []byte(`{"model":"claude-opus-5-5","system":{"bad":true},"metadata":{"user_id":"user_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef_account__session_11111111-2222-3333-4444-555555555555"},"messages":[]}`)
		got, injection := mimicAnthropicClaudeCodeBody(raw, account, 9001, ua, "claude-opus-5-5")
		require.Equal(t, raw, got)
		require.Zero(t, injection.Tokens)
	})
}

func TestApplyAnthropicClaudeCodeMimicHeaders(t *testing.T) {
	h := http.Header{}
	setHeaderRaw(h, "User-Agent", "python-httpx/0.28.1")
	setHeaderRaw(h, "anthropic-beta", "interleaved-thinking-2025-05-14, effort-2025-11-24")
	applyAnthropicClaudeCodeMimicHeaders(h, "claude-cli/2.1.302 (external, cli)")
	require.Equal(t, "claude-cli/2.1.302 (external, cli)", getHeaderRaw(h, "User-Agent"))
	require.Equal(t, "cli", getHeaderRaw(h, "x-app"))
	require.Equal(t, "claude-code-20250219,interleaved-thinking-2025-05-14,effort-2025-11-24", getHeaderRaw(h, "anthropic-beta"))
	require.Equal(t, "2023-06-01", getHeaderRaw(h, "anthropic-version"))

	cc := http.Header{}
	setHeaderRaw(cc, "User-Agent", "claude-cli/2.1.250 (external, sdk-ts)")
	setHeaderRaw(cc, "x-app", "sdk")
	setHeaderRaw(cc, "anthropic-beta", "oauth-2025-04-20,claude-code-20250219")
	applyAnthropicClaudeCodeMimicHeaders(cc, "claude-cli/2.1.302 (external, cli)")
	require.Equal(t, "claude-cli/2.1.250 (external, sdk-ts)", getHeaderRaw(cc, "User-Agent"), "已经是 claude-cli 的不换")
	require.Equal(t, "sdk", getHeaderRaw(cc, "x-app"))
	require.Equal(t, "oauth-2025-04-20,claude-code-20250219", getHeaderRaw(cc, "anthropic-beta"), "已有 claude-code beta 不重复补")

	setHeaderRaw(cc, "anthropic-beta", "oauth-2025-04-20, claude-code-20250219, effort-2025-11-24")
	applyAnthropicClaudeCodeMimicHeaders(cc, "claude-cli/2.1.302 (external, cli)")
	require.Equal(t, "oauth-2025-04-20, claude-code-20250219, effort-2025-11-24", getHeaderRaw(cc, "anthropic-beta"))
}

func TestBuildUpstreamRequest_AnthropicMimicClaudeCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &GatewayService{cfg: &config.Config{}}
	clientBody := []byte(`{"model":"claude-opus-5-5","max_tokens":128,"system":"Answer briefly.","messages":[{"role":"user","content":"hi"}]}`)

	t.Run("standard path passes the Claude Code only check and records the injection", func(t *testing.T) {
		account := newMimicClaudeCodeAccountForTest(3001, nil)
		c := newPlainClientContext(clientBody)
		req, wireBody, err := svc.buildUpstreamRequest(context.Background(), c, account, clientBody, "relay-key", "apikey", "claude-opus-5-5", true, false)
		require.NoError(t, err)

		requireClaudeCodeOnlyGroupAccepts(t, req, wireBody)
		require.Contains(t, getHeaderRaw(req.Header, "anthropic-beta"), "interleaved-thinking-2025-05-14", "客户端的能力 beta 保留")
		require.Equal(t, "Answer briefly.", gjson.GetBytes(wireBody, "system.1.text").String())

		value, ok := c.Get(claudeCodeMimicInjectionKey)
		require.True(t, ok)
		require.Equal(t, claudeCodeMimicInjection{AccountID: 3001, Tokens: 23}, value)
	})

	t.Run("passthrough path passes the Claude Code only check", func(t *testing.T) {
		account := newMimicClaudeCodeAccountForTest(3002, map[string]any{"anthropic_passthrough": true})
		c := newPlainClientContext(clientBody)
		req, wireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(context.Background(), c, account, clientBody, "relay-key")
		require.NoError(t, err)

		requireClaudeCodeOnlyGroupAccepts(t, req, wireBody)
		value, _ := c.Get(claudeCodeMimicInjectionKey)
		require.Equal(t, claudeCodeMimicInjection{AccountID: 3002, Tokens: 23}, value)
	})

	t.Run("count_tokens only gets headers so the count stays exact", func(t *testing.T) {
		account := newMimicClaudeCodeAccountForTest(3003, map[string]any{"anthropic_passthrough": true})
		ptReq, err := svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(context.Background(), newPlainClientContext(clientBody), account, clientBody, "relay-key")
		require.NoError(t, err)
		ptBody, err := io.ReadAll(ptReq.Body)
		require.NoError(t, err)
		require.True(t, claudeCodeUAPattern.MatchString(getHeaderRaw(ptReq.Header, "User-Agent")))
		require.Equal(t, "Answer briefly.", gjson.GetBytes(ptBody, "system").String())

		stdAccount := newMimicClaudeCodeAccountForTest(3004, nil)
		stdReq, stdBody, err := svc.buildCountTokensRequest(context.Background(), newPlainClientContext(clientBody), stdAccount, clientBody, "relay-key", "apikey", "claude-opus-5-5", false)
		require.NoError(t, err)
		require.True(t, claudeCodeUAPattern.MatchString(getHeaderRaw(stdReq.Header, "User-Agent")))
		require.Equal(t, "cli", getHeaderRaw(stdReq.Header, "x-app"))
		require.Equal(t, "Answer briefly.", gjson.GetBytes(stdBody, "system").String())
	})

	t.Run("real Claude Code client is forwarded unchanged", func(t *testing.T) {
		ccBody := []byte(`{"model":"claude-opus-5-5","system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"metadata":{"user_id":"user_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef_account__session_11111111-2222-3333-4444-555555555555"},"messages":[{"role":"user","content":"hi"}]}`)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(ccBody))
		c.Request.Header.Set("User-Agent", "claude-cli/2.1.302 (external, cli)")
		c.Request.Header.Set("x-app", "cli")
		c.Request.Header.Set("anthropic-beta", "claude-code-20250219,interleaved-thinking-2025-05-14")

		account := newMimicClaudeCodeAccountForTest(3005, map[string]any{"anthropic_passthrough": true})
		req, wireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(context.Background(), c, account, ccBody, "relay-key")
		require.NoError(t, err)
		require.Equal(t, "claude-cli/2.1.302 (external, cli)", getHeaderRaw(req.Header, "User-Agent"))
		require.Equal(t, "claude-code-20250219,interleaved-thinking-2025-05-14", getHeaderRaw(req.Header, "anthropic-beta"))
		require.JSONEq(t, string(ccBody), string(wireBody))
		value, _ := c.Get(claudeCodeMimicInjectionKey)
		require.Zero(t, value.(claudeCodeMimicInjection).Tokens)
	})

	t.Run("account header override keeps the final say on anthropic-beta", func(t *testing.T) {
		account := newMimicClaudeCodeAccountForTest(3006, nil)
		account.Credentials["header_override_enabled"] = true
		account.Credentials["header_overrides"] = map[string]any{"anthropic-beta": "claude-code-20250219,custom-beta"}
		req, _, err := svc.buildUpstreamRequest(context.Background(), newPlainClientContext(clientBody), account, clientBody, "relay-key", "apikey", "claude-opus-5-5", true, false)
		require.NoError(t, err)
		require.Equal(t, "claude-code-20250219,custom-beta", getHeaderRaw(req.Header, "anthropic-beta"))
	})

	t.Run("switch off leaves a plain client request alone", func(t *testing.T) {
		account := newMimicClaudeCodeAccountForTest(3007, map[string]any{"anthropic_mimic_claude_code": false})
		c := newPlainClientContext(clientBody)
		req, wireBody, err := svc.buildUpstreamRequest(context.Background(), c, account, clientBody, "relay-key", "apikey", "claude-opus-5-5", true, false)
		require.NoError(t, err)
		require.Equal(t, "python-httpx/0.28.1", getHeaderRaw(req.Header, "User-Agent"))
		require.Equal(t, "Answer briefly.", gjson.GetBytes(wireBody, "system").String())
		value, _ := c.Get(claudeCodeMimicInjectionKey)
		require.Equal(t, claudeCodeMimicInjection{AccountID: 3007}, value)
	})
}

func TestForward_AnthropicMimicClaudeCode_PassthroughAttachesInjection(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "python-httpx/0.28.1")
	c.Set("api_key", &APIKey{ID: 9001})

	body := []byte(`{"model":"claude-opus-5-5","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"ping"}]}`)
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5-5", Stream: true}

	upstreamSSE := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_011Cmimic","usage":{"input_tokens":34,"output_tokens":1}}}`,
		"",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		"",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(withAnthropicFixtureContent(upstreamSSE))),
	}}
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}
	account := newMimicClaudeCodeAccountForTest(4001, map[string]any{"anthropic_passthrough": true})

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)

	requireClaudeCodeOnlyGroupAccepts(t, upstream.lastReq, upstream.lastBody)
	require.Contains(t, rec.Body.String(), `"input_tokens":34`, "返回给客户端的 usage 保持上游原值")
	require.Equal(t, 34, result.Usage.InputTokens)
	require.Equal(t, claudeCodeMimicInjection{AccountID: 4001, Tokens: 24}, result.claudeCodeMimic)

	require.Equal(t, 24, deductClaudeCodeMimicUsage(result))
	require.Equal(t, 10, result.Usage.InputTokens, "扣回后等于官方裸 ping 的 10")
}

func TestAttachClaudeCodeMimicInjection_IgnoresOtherAccounts(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	setClaudeCodeMimicInjection(c, claudeCodeMimicInjection{AccountID: 1, Tokens: 24})

	other := &ForwardResult{}
	attachClaudeCodeMimicInjection(c, newMimicClaudeCodeAccountForTest(2, nil), other)
	require.Zero(t, other.claudeCodeMimic.Tokens, "换号重试后不能套用上一个账号的记录")

	same := &ForwardResult{}
	attachClaudeCodeMimicInjection(c, newMimicClaudeCodeAccountForTest(1, nil), same)
	require.Equal(t, 24, same.claudeCodeMimic.Tokens)

	for _, flag := range []any{nil, false} {
		account := newMimicClaudeCodeAccountForTest(1, map[string]any{"anthropic_mimic_claude_code_deduct_tokens": flag})
		normal := &ForwardResult{Usage: ClaudeUsage{InputTokens: 34}}
		attachClaudeCodeMimicInjection(c, account, normal)
		require.Zero(t, deductClaudeCodeMimicUsage(normal))
		require.Equal(t, 34, normal.Usage.InputTokens, "normal billing retains upstream usage")
	}
}

func TestForwardBridges_AnthropicMimicClaudeCode(t *testing.T) {
	for _, deduct := range []bool{false, true} {
		for _, chat := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				protocol := "responses"
				if chat {
					protocol = "chat"
				}
				t.Run(protocol+"/stream="+strconv.FormatBool(stream)+"/deduct="+strconv.FormatBool(deduct), func(t *testing.T) {
					body := `{"model":"public-claude","input":"ping","stream":` + strconv.FormatBool(stream) + `}`
					if chat {
						body = `{"model":"public-claude","messages":[{"role":"user","content":"ping"}],"stream":` + strconv.FormatBool(stream) + `}`
					}
					c := newPlainClientContext([]byte(body))
					payload := strings.Join([]string{
						`event: message_start`,
						`data: {"type":"message_start","message":{"id":"msg_bridge_mimic","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":34,"output_tokens":1}}}`,
						``,
						`event: content_block_start`,
						`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
						``,
						`event: content_block_delta`,
						`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
						``,
						`event: content_block_stop`,
						`data: {"type":"content_block_stop","index":0}`,
						``,
						`event: message_delta`,
						`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
						``,
						`event: message_stop`,
						`data: {"type":"message_stop"}`,
						``,
					}, "\n")
					upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:       io.NopCloser(strings.NewReader(payload)),
					}}
					svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}
					account := newMimicClaudeCodeAccountForTest(4002, map[string]any{"anthropic_mimic_claude_code_deduct_tokens": deduct})
					account.Credentials["model_mapping"] = map[string]any{"public-claude": "claude-opus-5-5"}
					var result *ForwardResult
					var err error
					if chat {
						result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(body), nil)
					} else {
						result, err = svc.ForwardAsResponses(context.Background(), c, account, []byte(body), nil)
					}
					require.NoError(t, err)
					require.NotNil(t, result)
					requireClaudeCodeOnlyGroupAccepts(t, upstream.lastReq, upstream.lastBody)
					require.Equal(t, "claude-opus-5-5", gjson.GetBytes(upstream.lastBody, "model").String())
					require.Equal(t, 34, result.Usage.InputTokens)
					if deduct {
						require.Equal(t, 24, deductClaudeCodeMimicUsage(result))
						require.Equal(t, 10, result.Usage.InputTokens)
					} else {
						require.Zero(t, deductClaudeCodeMimicUsage(result))
						require.Equal(t, 34, result.Usage.InputTokens)
					}
				})
			}
		}
	}
}

func TestDeductClaudeCodeMimicUsage(t *testing.T) {
	t.Run("uncached identity comes off input and only once", func(t *testing.T) {
		result := &ForwardResult{Usage: ClaudeUsage{InputTokens: 100, OutputTokens: 7}, claudeCodeMimic: claudeCodeMimicInjection{Tokens: 24}}
		require.Equal(t, 24, deductClaudeCodeMimicUsage(result))
		require.Equal(t, ClaudeUsage{InputTokens: 76, OutputTokens: 7}, result.Usage)
		require.Zero(t, deductClaudeCodeMimicUsage(result), "重复记账不能扣两次")
		require.Equal(t, 76, result.Usage.InputTokens)
	})

	t.Run("cache hit comes off cache read", func(t *testing.T) {
		result := &ForwardResult{
			Usage:           ClaudeUsage{InputTokens: 16, CacheReadInputTokens: 12728},
			claudeCodeMimic: claudeCodeMimicInjection{Tokens: 23, Cached: true},
		}
		deductClaudeCodeMimicUsage(result)
		require.Equal(t, ClaudeUsage{InputTokens: 16, CacheReadInputTokens: 12705}, result.Usage)
	})

	t.Run("cache miss comes off cache creation and its ttl bucket", func(t *testing.T) {
		result := &ForwardResult{
			Usage:           ClaudeUsage{InputTokens: 16, CacheCreationInputTokens: 12728, CacheCreation1hTokens: 12728},
			claudeCodeMimic: claudeCodeMimicInjection{Tokens: 23, Cached: true, CacheTTL1h: true},
		}
		deductClaudeCodeMimicUsage(result)
		require.Equal(t, ClaudeUsage{InputTokens: 16, CacheCreationInputTokens: 12705, CacheCreation1hTokens: 12705}, result.Usage)

		split := &ForwardResult{
			Usage:           ClaudeUsage{CacheCreationInputTokens: 40, CacheCreation5mTokens: 30, CacheCreation1hTokens: 10},
			claudeCodeMimic: claudeCodeMimicInjection{Tokens: 23, Cached: true, CacheTTL1h: true},
		}
		deductClaudeCodeMimicUsage(split)
		require.Equal(t, ClaudeUsage{CacheCreationInputTokens: 17, CacheCreation5mTokens: 17, CacheCreation1hTokens: 0}, split.Usage)
	})

	t.Run("prompt below the cache minimum falls back to input", func(t *testing.T) {
		result := &ForwardResult{Usage: ClaudeUsage{InputTokens: 40}, claudeCodeMimic: claudeCodeMimicInjection{Tokens: 23, Cached: true}}
		deductClaudeCodeMimicUsage(result)
		require.Equal(t, 17, result.Usage.InputTokens)
	})

	t.Run("never goes negative", func(t *testing.T) {
		result := &ForwardResult{Usage: ClaudeUsage{InputTokens: 5}, claudeCodeMimic: claudeCodeMimicInjection{Tokens: 24}}
		require.Equal(t, 5, deductClaudeCodeMimicUsage(result))
		require.Zero(t, result.Usage.InputTokens)
	})
}

func TestGatewayServiceRecordUsage_DeductsClaudeCodeMimicIdentity(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:       "mimic_identity_deduct",
			Usage:           ClaudeUsage{InputTokens: 34, OutputTokens: 6},
			Model:           "claude-opus-5-5",
			Duration:        time.Second,
			claudeCodeMimic: claudeCodeMimicInjection{AccountID: 701, Tokens: 24},
		},
		APIKey:  &APIKey{ID: 501, Quota: 100},
		User:    &User{ID: 601},
		Account: &Account{ID: 701},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, 10, usageRepo.lastLog.InputTokens)
	require.Equal(t, 6, usageRepo.lastLog.OutputTokens)

	forced := &openAIRecordUsageLogRepoStub{inserted: true}
	svc = newGatewayRecordUsageServiceForTest(forced, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	err = svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:       "mimic_identity_force_cache",
			Usage:           ClaudeUsage{InputTokens: 34, OutputTokens: 6},
			Model:           "claude-opus-5-5",
			Duration:        time.Second,
			claudeCodeMimic: claudeCodeMimicInjection{AccountID: 701, Tokens: 24},
		},
		APIKey:            &APIKey{ID: 501, Quota: 100},
		User:              &User{ID: 601},
		Account:           &Account{ID: 701},
		ForceCacheBilling: true,
	})
	require.NoError(t, err)
	require.Zero(t, forced.lastLog.InputTokens)
	require.Equal(t, 10, forced.lastLog.CacheReadTokens, "先扣身份句再按强制缓存计费")
}

func TestAccountTestService_AnthropicMimicClaudeCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newMimicClaudeCodeAccountForTest(5001, nil)
	repo := &stripIdentityTestAccountRepo{account: account}
	upstream := &stripIdentityCaptureUpstream{}
	svc := NewAccountTestService(repo, nil, nil, nil, nil, upstream, &config.Config{}, nil)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/5001/test", nil)
	require.NoError(t, svc.TestAccountConnection(c, 5001, "claude-opus-5-5", "", ""))
	require.NotNil(t, upstream.lastReq)

	requireClaudeCodeOnlyGroupAccepts(t, upstream.lastReq, upstream.lastBody)
	require.Equal(t, claude.DefaultUserAgent(), getHeaderRaw(upstream.lastReq.Header, "User-Agent"))
}
