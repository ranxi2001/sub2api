package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func excelImageToolResponse(t *testing.T, selected bool) *http.Response {
	t.Helper()
	output := []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "ready"}}}}
	if selected {
		envelope, _ := json.Marshal(map[string]any{"name": basispoints.ImageGenerationToolName, "arguments": map[string]any{"prompt": "a kitten drinking water"}})
		args, _ := json.Marshal(map[string]any{"code": string(envelope), "summary": "Generate image", "extended_summary": "Generate an image with native tool", "destructive": false, "references": []any{}})
		output = append(output, map[string]any{"type": "function_call", "id": "fc_private", "call_id": "call_private", "name": "run_officejs", "arguments": string(args), "status": "completed"})
	}
	wire, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_bps_generation", "model": "gpt-6-astra", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 10, "output_tokens": 2, "total_tokens": 12}}})
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: response.completed\ndata: " + string(wire) + "\n\n"))}
}

func excelImageNativeResponse() *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_native_image","object":"response","model":"gpt-6-astra","status":"completed","output":[{"type":"image_generation_call","id":"ig_native","status":"completed","result":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+ip1sAAAAASUVORK5CYII=","output_format":"png","size":"1024x1024"}],"usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23},"tool_usage":{"image_gen":{"input_tokens":7,"input_tokens_details":{"image_tokens":0,"text_tokens":7},"output_tokens":9,"output_tokens_details":{"image_tokens":9,"text_tokens":0},"total_tokens":16}}}`))}
}

func excelImageAccount() *Account {
	account := excelAccount()
	account.GroupIDs = []int64{4242}
	return account
}

func excelImageNativeSSEResponse() *http.Response {
	response := excelImageNativeResponse()
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	response.Header.Set("Content-Type", "text/event-stream")
	response.Body = io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + string(raw) + "}\n\n"))
	return response
}

func TestExcelBPSAutomaticImageGeneration(t *testing.T) {
	for _, test := range []struct {
		stream bool
		lite   bool
	}{
		{false, false}, {true, false}, {false, true}, {true, true},
	} {
		t.Run(fmt.Sprintf("stream=%v/lite=%v", test.stream, test.lite), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{responses: []*http.Response{excelImageToolResponse(t, true), excelImageNativeSSEResponse()}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			c, rec := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.98.0")
			if test.lite {
				c.Request.Header.Set("User-Agent", "Codex Desktop/0.155.0-alpha.16.4 (Mac OS 27.0.0; arm64) unknown (Codex Desktop; 26.917.71314)")
				c.Request.Header.Set("originator", "Codex Desktop")
				c.Request.Header.Set(responsesLiteHeader, "true")
			}
			account := excelImageAccount()
			account.Extra[featureKeyCodexImageGenerationBridge] = true
			acquired, released := 0, 0
			ctx := WithExcelBPSImageSlotAcquirer(context.Background(), func(ctx context.Context) (func(), bool) { acquired++; return func() { released++ }, true })
			c.Request = c.Request.WithContext(ctx)
			// The real HTTP handler seeds false for the outer text request. The
			// nested native image request must not inherit that billing hint.
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			SetOpenAIImageIntentHint(c, false)
			body := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","input":"generate a kitten drinking water","stream":%v}`, test.stream))
			result, err := svc.Forward(ctx, c, account, body)
			require.NoError(t, err, rec.Body.String())
			require.Len(t, upstream.requests, 2)
			require.Equal(t, "bps.openai.com", upstream.requests[0].URL.Host)
			require.Equal(t, "chatgpt.com", upstream.requests[1].URL.Host)
			require.Empty(t, upstream.requests[0].Header.Get(responsesLiteHeader))
			require.Empty(t, upstream.requests[1].Header.Get(responsesLiteHeader), "the hosted child must not inherit the Lite protocol")
			require.Equal(t, test.lite, isOpenAIResponsesLiteHeader(c.GetHeader(responsesLiteHeader)), "the parent request must stay unchanged")
			require.Contains(t, string(upstream.bodies[0]), basispoints.ImageGenerationToolName)
			require.Equal(t, "image_generation", gjson.GetBytes(upstream.bodies[1], "tools.0.type").String())
			require.Contains(t, gjson.GetBytes(upstream.bodies[1], "input").String(), "a kitten drinking water")
			require.Equal(t, 1, acquired)
			require.Equal(t, 1, released)
			require.True(t, account.IsExcelBPSEnabledForModel("gpt-6-astra"))
			require.Equal(t, 30, result.Usage.InputTokens)
			require.Equal(t, 5, result.Usage.OutputTokens)
			require.Equal(t, 9, result.Usage.ImageOutputTokens)
			require.Equal(t, 1, result.ImageCount)
			require.NotEmpty(t, result.BillingModel)
			outerIntent, known := getOpenAIImageIntentHint(c)
			require.True(t, known)
			require.False(t, outerIntent, "the child must not mutate the parent hint")
			require.Equal(t, "/basispoints/api/responses", result.UpstreamEndpoint)
			require.Contains(t, rec.Body.String(), "ig_bps_")
			require.NotContains(t, rec.Body.String(), "sub2api_generate_image")
		})
	}
}

func TestExcelBPSAutomaticImageGenerationGuards(t *testing.T) {
	for _, name := range []string{"ordinary chat", "ordinary Lite chat", "permission denied", "bridge disabled", "strip policy", "non codex", "compact", "missing limiter", "tool choice none"} {
		t.Run(name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: excelImageToolResponse(t, false)}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			c, rec := newOpenAIImageGenerationControlTestContext(name != "permission denied", "codex_cli_rs/0.98.0")
			account := excelImageAccount()
			account.Extra[featureKeyCodexImageGenerationBridge] = name != "bridge disabled"
			body := []byte(`{"model":"gpt-6-astra","input":"hello","stream":false}`)
			switch name {
			case "strip policy":
				account.Extra[featureKeyCodexImageGenerationExplicitToolPolicy] = "strip"
			case "non codex":
				c.Request.Header.Set("User-Agent", "generic-client")
			case "ordinary Lite chat":
				c.Request.Header.Set(responsesLiteHeader, "true")
			case "compact":
				c.Request.URL.Path = "/v1/responses/compact"
			case "missing limiter":
				svc.cfg.Gateway.ImageConcurrency.Enabled = true
			case "tool choice none":
				body = []byte(`{"model":"gpt-6-astra","input":"hello","tool_choice":"none"}`)
			}
			result, err := svc.Forward(c.Request.Context(), c, account, body)
			require.NoError(t, err, rec.Body.String())
			require.Zero(t, result.ImageCount)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, "bps.openai.com", upstream.lastReq.URL.Host)
			if name != "ordinary chat" && name != "ordinary Lite chat" {
				require.NotContains(t, string(upstream.lastBody), basispoints.ImageGenerationToolName)
			} else {
				require.Contains(t, string(upstream.lastBody), basispoints.ImageGenerationToolName)
			}
		})
	}
}

func TestExcelBPSAutomaticImageLimitRejectionDoesNotCallNative(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: excelImageToolResponse(t, true)}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, rec := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.98.0")
	account := excelImageAccount()
	account.Extra[featureKeyCodexImageGenerationBridge] = true
	acquired := 0
	ctx := WithExcelBPSImageSlotAcquirer(context.Background(), func(context.Context) (func(), bool) { acquired++; return nil, false })
	c.Request = c.Request.WithContext(ctx)
	result, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-6-astra","input":"draw a cat","stream":true}`))
	require.Error(t, err)
	require.Equal(t, 1, acquired)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "response.failed", result.UpstreamTerminalEvent)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Contains(t, rec.Body.String(), "basispoints_image_generation_failed")
}

func TestExcelBPSImageRecorderIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &excelBPSImageRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	n, err := w.Write([]byte(strings.Repeat("x", (32<<20)+1)))
	require.ErrorIs(t, err, io.ErrShortWrite)
	require.Zero(t, n)
	require.Zero(t, w.Body.Len())
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}
