package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func bpsWireToken(t *testing.T, auth map[string]string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"sub": "not-an-account-user", "https://api.openai.com/auth": auth})
	require.NoError(t, err)
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestExcelBPSWireIdentityUsesActualBearer(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth map[string]string
		want string
	}{
		{"account user", map[string]string{"chatgpt_user_id": "bps-user", "user_id": "other"}, "bps-user"},
		{"user fallback", map[string]string{"user_id": "bps-user"}, "bps-user"},
		{"missing user does not use sub", map[string]string{}, ""},
		{"invalid header", map[string]string{"user_id": "bad\r\nInjected: value"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, endpoint := range []string{basispoints.ResponsesURL, basispoints.ImagesGenerationsURL, "https://bps.openai.com/basispoints/api/attachments", "https://bps.openai.com/basispoints/api/responses/access?include_models=true"} {
				token := bpsWireToken(t, tc.auth)
				req, err := newExcelBPSRequestTo(context.Background(), endpoint, "application/json", nil, token, "fixture-workspace")
				require.NoError(t, err)
				require.Equal(t, tc.want, req.Header.Get("X-Openai-Account-User-Id"))
				require.Equal(t, "Bearer "+token, req.Header.Get("Authorization"))
				require.Equal(t, "fixture-workspace", req.Header.Get("X-Openai-Account-Id"))
				require.Empty(t, req.Header.Get("Cookie"))
			}
		})
	}
	req, err := newExcelBPSRequest(context.Background(), nil, "opaque-token", "fixture-workspace")
	require.NoError(t, err)
	require.Empty(t, req.Header.Get("X-Openai-Account-User-Id"))
}

func TestExcelBPSWireTierPolicyAndModelEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, tier, action, want string
		force                    bool
		status                   int
	}{
		{name: "priority", tier: "priority", want: "priority"},
		{name: "fast alias", tier: "fast", want: "priority"},
		{name: "default", tier: "default", want: "default"},
		{name: "auto", tier: "auto", want: "auto"},
		{name: "missing stays missing"},
		{name: "group force", force: true, want: "priority"},
		{name: "filter overrides group", force: true, action: BetaPolicyActionFilter},
		{name: "block", tier: "priority", action: BetaPolicyActionBlock, status: 403},
		{name: "unsupported is not silently dropped", tier: "flex", status: 400},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				settings := DefaultOpenAIFastPolicySettings()
				if tc.action != "" {
					settings.Rules = []OpenAIFastPolicyRule{{ServiceTier: OpenAIFastTierPriority, Action: tc.action, Scope: BetaPolicyScopeAll, FallbackAction: BetaPolicyActionPass}}
				}
				svc := openAIClientToolsTestService(nil)
				svc.settingService = newOpenAIGatewayServiceWithSettings(t, settings).settingService
				wire := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_wire\",\"status\":\"completed\",\"model\":\"gpt-5.6-luna\",\"service_tier\":\"default\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n"
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
				svc.httpUpstream = upstream
				input := map[string]any{"model": "public-astra", "stream": stream, "reasoning": map[string]any{"effort": "xhigh"}, "input": "fixture request"}
				if tc.tier != "" {
					input["service_tier"] = tc.tier
				}
				body, err := json.Marshal(input)
				require.NoError(t, err)
				account := excelAccount()
				account.Credentials["model_mapping"] = map[string]any{"public-astra": "gpt-6-astra"}
				account.Credentials["access_token"] = bpsWireToken(t, map[string]string{"chatgpt_account_id": "test-account", "user_id": "selected-bps-user"})
				account.Credentials["chatgpt_user_id"] = "stale-codex-user"
				ctx := context.Background()
				if tc.force {
					ctx = context.WithValue(ctx, ctxkey.Group, &Group{ID: 7, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, ForceOpenAIFast: true})
				}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
				c.Request.Header.Set("X-Openai-Account-User-Id", "untrusted-client-user")
				result, err := svc.Forward(ctx, c, account, body)
				if tc.status != 0 {
					require.Error(t, err)
					require.Equal(t, tc.status, rec.Code)
					require.Empty(t, upstream.requests)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Len(t, upstream.requests, 1)
				require.Equal(t, "gpt-6-astra", gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, "explicit", gjson.GetBytes(upstream.lastBody, "model_selection").String())
				require.Equal(t, "xhigh", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
				require.Equal(t, tc.want, gjson.GetBytes(upstream.lastBody, "service_tier").String())
				require.Equal(t, tc.want, optionalStringValue(result.ServiceTier))
				require.Equal(t, "selected-bps-user", upstream.lastReq.Header.Get("X-Openai-Account-User-Id"))
				require.Equal(t, "gpt-5.6-luna", result.UpstreamResponseModel, "never relabel provider model as requested Astra")
				require.Equal(t, "default", result.UpstreamResponseServiceTier, "keep provider declaration distinct from requested priority")
				require.Contains(t, rec.Body.String(), "gpt-5.6-luna")
				require.Equal(t, "gpt-6-astra", result.UpstreamModel)
			})
		}
	}
}
