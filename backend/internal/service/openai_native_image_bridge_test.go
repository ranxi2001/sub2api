package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func nativeImageDesktopBody(stream bool) []byte {
	body, _ := json.Marshal(map[string]any{
		"model": "gpt-6-astra", "stream": stream,
		"input": "generate a kitten drinking water", "instructions": "Help the user.",
		"tools": []any{map[string]any{"type": "namespace", "name": "functions", "tools": []any{
			map[string]any{"type": "custom", "name": "exec", "description": "Run JavaScript; ALL_TOOLS; generatedImage(result: {image_url: string})", "format": map[string]any{"type": "text"}},
		}}},
	})
	return body
}

func nativeImageDesktopResponse(t *testing.T, selected bool, stream bool) *http.Response {
	t.Helper()
	output := []any{map[string]any{"id": "msg_text", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "ready"}}}}
	if selected {
		args, _ := json.Marshal(map[string]any{"prompt": "a kitten drinking water"})
		output = []any{map[string]any{"type": "function_call", "id": "fc_native_image", "call_id": "call_native_image", "name": basispoints.ImageGenerationToolName, "arguments": string(args), "status": "completed"}}
	}
	response := map[string]any{"id": "resp_native_desktop", "model": "gpt-6-astra", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 10, "output_tokens": 2}}
	contentType := "application/json"
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	if stream {
		wire, err := json.Marshal(map[string]any{"type": "response.completed", "response": response})
		require.NoError(t, err)
		raw = []byte("event: response.completed\ndata: " + string(wire) + "\n\n")
		contentType = "text/event-stream"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(string(raw)))}
}

func TestNativeCodexImageBridgeBPSDisabled(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, lite := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("passthrough=%v/lite=%v/stream=%v", passthrough, lite, stream), func(t *testing.T) {
					// OAuth passthrough normalizes stream=true before sending upstream,
					// even when the inbound client requests a JSON response.
					upstream := &httpUpstreamRecorder{responses: []*http.Response{nativeImageDesktopResponse(t, true, stream || passthrough), excelImageNativeSSEResponse()}}
					svc := newOpenAIImageGenerationControlTestService(upstream)
					c, recorder := newOpenAIImageGenerationControlTestContext(true, "Codex Desktop/0.155.0-alpha.16.4 (Mac OS 27.0.0; arm64) unknown (Codex Desktop; 26.917.71314)")
					c.Request.Header.Set("originator", "Codex Desktop")
					if lite {
						c.Request.Header.Set(responsesLiteHeader, "true")
					}
					SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
					SetOpenAIImageIntentHint(c, false)
					account := excelImageAccount()
					account.Extra["openai_excel_bps"] = false
					account.Extra["openai_passthrough"] = passthrough
					account.Extra["openai_oauth_passthrough"] = passthrough
					account.Extra[featureKeyCodexImageGenerationBridge] = true
					acquired, released := 0, 0
					ctx := WithExcelBPSImageSlotAcquirer(context.Background(), func(context.Context) (func(), bool) { acquired++; return func() { released++ }, true })
					c.Request = c.Request.WithContext(ctx)
					result, err := svc.Forward(ctx, c, account, nativeImageDesktopBody(stream))
					require.NoError(t, err, recorder.Body.String())
					require.NotNil(t, result)
					require.Len(t, upstream.requests, 2)
					for _, req := range upstream.requests {
						require.Equal(t, "chatgpt.com", req.URL.Host)
						require.NotContains(t, req.URL.Path, "basispoints")
					}
					require.Equal(t, lite, isOpenAIResponsesLiteHeader(upstream.requests[0].Header.Get(responsesLiteHeader)))
					require.Empty(t, upstream.requests[1].Header.Get(responsesLiteHeader))
					require.Contains(t, string(upstream.bodies[0]), basispoints.ImageGenerationToolName)
					require.False(t, gjson.GetBytes(upstream.bodies[0], "tools.#(type==\"image_generation\")").Exists())
					require.Equal(t, "image_generation", gjson.GetBytes(upstream.bodies[1], "tools.0.type").String())
					require.Equal(t, 1, acquired)
					require.Equal(t, 1, released)
					require.Equal(t, 1, result.ImageCount)
					require.Equal(t, 30, result.Usage.InputTokens)
					require.NotContains(t, result.UpstreamEndpoint, "basispoints")
					require.Contains(t, recorder.Body.String(), "sub2api image file delivery v3")
					require.NotContains(t, recorder.Body.String(), basispoints.ImageGenerationToolName)
					require.False(t, account.IsExcelBPSEnabledForModel("gpt-6-astra"))
				})
			}
		}
	}
}

func TestNativeCodexImageBridgeRespectsPermissionsAndPlainChat(t *testing.T) {
	for _, mode := range []string{"text", "bridge-off", "group-off", "strip", "none", "explicit-client", "compact"} {
		t.Run(mode, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{responses: []*http.Response{nativeImageDesktopResponse(t, false, true)}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			c, recorder := newOpenAIImageGenerationControlTestContext(mode != "group-off", "codex_cli_rs/0.98.0")
			c.Request.Header.Set(responsesLiteHeader, "true")
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			SetOpenAIImageIntentHint(c, false)
			account := excelImageAccount()
			account.Extra["openai_excel_bps"] = false
			account.Extra[featureKeyCodexImageGenerationBridge] = mode != "bridge-off"
			if mode == "strip" {
				account.Extra[featureKeyCodexImageGenerationExplicitToolPolicy] = "strip"
			}
			var body map[string]any
			require.NoError(t, json.Unmarshal(nativeImageDesktopBody(true), &body))
			body["input"] = "hello"
			if mode == "none" {
				body["tool_choice"] = "none"
			}
			if mode == "explicit-client" {
				body["tools"] = append(body["tools"].([]any), map[string]any{"type": "function", "name": "image_gen.imagegen", "parameters": map[string]any{"type": "object"}})
			}
			if mode == "compact" {
				c.Request.URL.Path = "/v1/responses/compact"
			}
			raw, _ := json.Marshal(body)
			result, err := svc.Forward(context.Background(), c, account, raw)
			require.NoError(t, err, recorder.Body.String())
			require.NotNil(t, result)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, mode == "text", strings.Contains(string(upstream.bodies[0]), basispoints.ImageGenerationToolName))
			require.Zero(t, result.ImageCount)
			require.Contains(t, recorder.Body.String(), "ready")
			require.False(t, account.IsExcelBPSEnabledForModel("gpt-6-astra"))
		})
	}
}

func TestNativeCodexImageBridgeSlowGenerationCommitsBeforeFirstOutputTimeout(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough=%v", passthrough), func(t *testing.T) {
			child := excelImageNativeSSEResponse()
			terminal, err := io.ReadAll(child.Body)
			require.NoError(t, err)
			reader, writer := io.Pipe()
			child.Body = reader
			defer reader.Close()
			defer writer.Close()
			go func() {
				defer writer.Close()
				if _, err := io.WriteString(writer, "event: response.image_generation_call.in_progress\ndata: {\"type\":\"response.image_generation_call.in_progress\",\"item_id\":\"ig_slow\",\"output_index\":0}\n\n"); err != nil {
					return
				}
				time.Sleep(1200 * time.Millisecond)
				_, _ = writer.Write(terminal)
			}()
			upstream := &httpUpstreamRecorder{responses: []*http.Response{nativeImageDesktopResponse(t, true, true), child}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
			c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.98.0")
			c.Request.Header.Set(responsesLiteHeader, "true")
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			SetOpenAIImageIntentHint(c, false)
			account := excelImageAccount()
			account.Extra["openai_excel_bps"] = false
			account.Extra["openai_passthrough"] = passthrough
			account.Extra[featureKeyCodexImageGenerationBridge] = true
			result, err := svc.Forward(context.Background(), c, account, nativeImageDesktopBody(true))
			require.NoError(t, err, recorder.Body.String())
			require.Equal(t, 1, result.ImageCount)
			require.Len(t, upstream.requests, 2)
			require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: response.output_item.added\n"))
			require.Contains(t, recorder.Body.String(), "sub2api image file delivery v3")
		})
	}
}
