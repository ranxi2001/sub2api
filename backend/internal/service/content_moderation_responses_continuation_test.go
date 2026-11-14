package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/typesafe"
	"github.com/stretchr/testify/require"
)

func TestExtractContentModerationInput_ResponsesContinuation(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		images           []string
	}{
		{
			name: "latest turn excludes earlier tools and assistant text",
			body: `{"input":[{"role":"user","content":"old"},{"type":"function_call_output","output":"old result"},{"role":"user","content":"current"},{"role":"assistant","content":"generated"},{"type":"function_call","arguments":"opaque args"},{"type":"function_call_output","output":"result one"},{"type":"custom_tool_call_output","output":"result two"},{"type":"reasoning","encrypted_content":"opaque"}]}`,
			want: "current result one result two",
		},
		{
			name: "delta only tool results",
			body: `{"previous_response_id":"resp_fixture","input":[{"type":"function_call_output","call_id":"call_a","output":"result one"},{"type":"custom_tool_call_output","call_id":"call_b","output":"result two"}]}`,
			want: "result one result two",
		},
		{
			name: "tool result object",
			body: `{"input":{"type":"function_call_output","output":"result"}}`,
			want: "result",
		},
		{
			name:   "multimodal tool result",
			body:   `{"input":[{"role":"user","content":"current"},{"type":"function_call_output","output":[{"type":"input_text","text":"result"},{"type":"input_image","image_url":"https://fixture.invalid/image.png"}]}]}`,
			want:   "current result",
			images: []string{"https://fixture.invalid/image.png"},
		},
		{
			name: "latest user closes old tool turn",
			body: `{"input":[{"role":"user","content":"old"},{"type":"function_call_output","output":"old result"},{"role":"user","content":"current"}]}`,
			want: "current",
		},
		{
			name: "empty trailing user does not hide readable input",
			body: `{"input":[{"role":"user","content":"current"},{"role":"user","content":" "},{"type":"function_call_output","output":"result"}]}`,
			want: "current result",
		},
		{
			name: "roleless input text before assistant tail",
			body: `{"input":[{"type":"input_text","text":"current"},{"role":"assistant","content":"generated"}]}`,
			want: "current",
		},
		{
			name: "assistant and opaque data are not user input",
			body: `{"instructions":"system instructions","input":[{"role":"developer","content":"developer instructions"},{"role":"assistant","content":"generated"},{"type":"reasoning","encrypted_content":"opaque"},{"type":"item_reference","id":"item_fixture"}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)
			require.Equal(t, tc.want, input.Text)
			if len(tc.images) == 0 {
				require.Empty(t, input.Images)
			} else {
				require.Equal(t, tc.images, input.Images)
			}
			require.Equal(t, tc.want, extractContentModerationKeywordText(ContentModerationProtocolOpenAIResponses, body))
			require.Equal(t, tc.body, string(body), "audit extraction must not mutate the forwarded body")
		})
	}
}

func TestContentModerationCheck_ResponsesContinuationTypeSafePreBlock(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"history", `{"input":[{"role":"user","content":"fixture prompt"},{"role":"assistant","content":"generated"},{"type":"function_call_output","output":"fixture result"}]}`, "fixture prompt fixture result"},
		{"delta", `{"previous_response_id":"resp_fixture","input":[{"type":"function_call_output","output":"fixture result"}]}`, "fixture result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, blocked := range []bool{false, true} {
				t.Run(map[bool]string{false: "allow", true: "block"}[blocked], func(t *testing.T) {
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						var request typesafe.Request
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
							t.Error(err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						require.Equal(t, "/v1/systemone", r.URL.Path)
						require.Equal(t, tc.want, request.State)
						answers := map[string]any{}
						for category := range request.Questions {
							score := 0.01
							if blocked && category == "sexual" {
								score = 0.99
							}
							answers[category] = map[string]any{"type": "noul", "noul": score}
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixture", "answers": answers})
					}))
					defer server.Close()

					cfg := defaultContentModerationConfig()
					cfg.Enabled = true
					cfg.Mode = ContentModerationModePreBlock
					cfg.KeywordBlockingMode = ContentModerationKeywordModeAPIOnly
					cfg.Engine = ContentModerationEngineTypeSafe
					cfg.TypeSafe = moderationEngineDefaults(ContentModerationEngineTypeSafe)
					cfg.TypeSafe.BaseURL = server.URL
					cfg.TypeSafe.APIKeys = []string{"fixture-audit-key"}
					cfg.BlockStatus = http.StatusForbidden
					raw, err := json.Marshal(cfg)
					require.NoError(t, err)
					svc := NewContentModerationService(&contentModerationTestSettingRepo{values: map[string]string{
						SettingKeyRiskControlEnabled: "true", SettingKeyContentModerationConfig: string(raw),
					}}, &contentModerationTestRepo{}, &contentModerationTestHashCache{}, nil, nil, nil, nil, nil)
					decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
						UserID: 1001, RequestID: "fixture-continuation", Endpoint: "/v1/responses",
						Provider: "openai", Model: "fixture-model", Protocol: ContentModerationProtocolOpenAIResponses,
						Body: []byte(tc.body),
					})
					require.NoError(t, err)
					require.Equal(t, int32(1), calls.Load(), "tool continuations must reach the configured auditor")
					require.Equal(t, blocked, decision.Blocked)
					require.Equal(t, !blocked, decision.Allowed)
					if blocked {
						require.Equal(t, http.StatusForbidden, decision.StatusCode)
						require.Equal(t, ContentModerationActionBlock, decision.Action)
					}
				})
			}
		})
	}
}
