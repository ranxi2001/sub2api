//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Includes repeated fields, nested reference arrays and bools: indexing the
// native blocks and merging them would corrupt signatures and closed flags.
var mistralReviewFrames = []string{
	`[{"type":"thinking","thinking":[{"type":"text","text":"Think"},{"type":"reference","reference_ids":["ref-1"]}],"signature":"sig-1","closed":false}]`,
	`[{"type":"thinking","thinking":[{"type":"text","text":" more"},{"type":"reference","reference_ids":["ref-2"]}],"signature":"sig-2","closed":false}]`,
	`[{"type":"thinking","thinking":[],"signature":"sig-3","closed":true},{"type":"text","text":"Hello","references":[{"id":"ref-1"}]}]`,
}

func TestMistralReviewSDKAccumulation(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	var deltas []json.RawMessage
	for _, frame := range mistralReviewFrames {
		out, err := normalizeMistralChatResponse(account, []byte(`{"choices":[{"index":0,"delta":{"content":`+frame+`}}]}`), false)
		require.NoError(t, err)
		deltas = append(deltas, json.RawMessage(gjson.GetBytes(out, "choices.0.delta").Raw))
	}
	deltas = append(deltas, json.RawMessage(`{"content":" world"}`))
	data, err := json.Marshal(deltas)
	require.NoError(t, err)
	cmd := exec.Command("python3", "testdata/mistral/accumulate.py")
	cmd.Stdin = bytes.NewReader(data)
	accumulated, err := cmd.CombinedOutput()
	require.NoError(t, err, "pinned SDK accumulate_delta: %s", accumulated)
	require.Equal(t, "Hello world", gjson.GetBytes(accumulated, "content").String())
	require.Equal(t, "Think more", gjson.GetBytes(accumulated, "reasoning_content").String())
	extension := gjson.GetBytes(accumulated, "mistral_content")
	require.Equal(t, gjson.String, extension.Type)
	frames := strings.Split(strings.TrimSpace(extension.String()), "\n")
	require.Len(t, frames, len(mistralReviewFrames))
	for i, frame := range frames {
		require.JSONEq(t, mistralReviewFrames[i], frame)
	}
	// Replay the actual SDK accumulator, preserving every native field.
	var message map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(accumulated, &message))
	message["role"] = json.RawMessage(`"assistant"`)
	msg, err := json.Marshal(message)
	require.NoError(t, err)
	out, err := normalizeMistralChatRequest(account, []byte(`{"messages":[`+string(msg)+`]}`))
	require.NoError(t, err)
	var want []json.RawMessage
	for _, frame := range mistralReviewFrames {
		var blocks []json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(frame), &blocks))
		want = append(want, blocks...)
	}
	want = append(want, json.RawMessage(`{"type":"text","text":" world"}`))
	expected, err := json.Marshal(want)
	require.NoError(t, err)
	require.JSONEq(t, string(expected), gjson.GetBytes(out, "messages.0.content").Raw)
}

func TestMistralReviewHistoryReplay(t *testing.T) {
	native := `[{"type":"thinking","thinking":[{"type":"text","text":"Think"},{"type":"reference","reference_ids":["ref"]}],"signature":"sig","closed":true},{"type":"text","text":"Hello","references":[{"id":"ref"}]}]`
	encoded, err := json.Marshal(native + "\n")
	require.NoError(t, err)
	for _, ext := range []struct{ name, value string }{{"legacy", native}, {"jsonl", string(encoded)}} {
		t.Run(ext.name, func(t *testing.T) {
			for _, content := range []string{`"Hello world"`, `"Hello"`, `null`, `""`} {
				t.Run(content, func(t *testing.T) {
					msg := `{"role":"assistant","content":` + content + `,"mistral_content":` + ext.value + `,"reasoning_content":"Think","tool_calls":[{"id":"call-1","type":"function","function":{"name":"f","arguments":"{}"}}]}`
					body := []byte(`{"messages":[{"role":"user","content":"first"},` + msg + `,{"role":"tool","tool_call_id":"call-1","content":"ok"},{"role":"user","content":"next"},` + msg + `]}`)
					out, err := normalizeMistralChatRequest(&Account{Platform: PlatformMistral}, body)
					require.NoError(t, err)
					for _, i := range []int{1, 4} {
						p := fmt.Sprintf("messages.%d", i)
						want := native
						if content == `"Hello world"` {
							want = strings.TrimSuffix(native, "]") + `,{"type":"text","text":" world"}]`
						}
						require.JSONEq(t, want, gjson.GetBytes(out, p+".content").Raw)
						require.Equal(t, "call-1", gjson.GetBytes(out, p+".tool_calls.0.id").String())
						require.False(t, gjson.GetBytes(out, p+".mistral_content").Exists())
						require.False(t, gjson.GetBytes(out, p+".reasoning_content").Exists())
					}
					again, err := normalizeMistralChatRequest(&Account{Platform: PlatformMistral}, out)
					require.NoError(t, err)
					require.Equal(t, out, again)
				})
			}
		})
	}
	for _, saved := range []string{native, string(encoded)} {
		_, err := normalizeMistralChatRequest(&Account{Platform: PlatformMistral}, []byte(`{"messages":[{"role":"assistant","content":"different","mistral_content":`+saved+`}]}`))
		require.ErrorIs(t, err, errMistralContent, "conflicting history must fail explicitly")
	}
	for _, content := range []string{`"plain"`, `null`, `[]`, `""`} {
		body := []byte(`{"messages":[{"role":"assistant","content":` + content + `}]}`)
		out, err := normalizeMistralChatRequest(&Account{Platform: PlatformMistral}, body)
		require.NoError(t, err)
		require.Equal(t, body, out)
	}
}

func TestMistralReviewHistoryBoundaries(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	for _, saved := range []string{`null`, `42`, `""`, `"not json"`, `"{}\n"`, `"[null]\n"`, `"[{\"type\":\"unknown\"}]\n"`} {
		_, err := normalizeMistralChatRequest(account, []byte(`{"messages":[{"role":"assistant","content":"","mistral_content":`+saved+`}]}`))
		require.ErrorIs(t, err, errMistralContent)
	}
	// Native content and an equivalent extension may differ in field ordering.
	body := []byte(`{"messages":[{"role":"assistant","content":[{"text":"ok","type":"text","extra":9007199254740993}],"mistral_content":[{"type":"text","text":"ok","extra":9007199254740993}]}]}`)
	out, err := normalizeMistralChatRequest(account, body)
	require.NoError(t, err)
	require.Contains(t, string(out), "9007199254740993")
	require.JSONEq(t, gjson.GetBytes(body, "messages.0.content").Raw, gjson.GetBytes(out, "messages.0.content").Raw)
	// Distinct integers beyond float64 precision remain a conflict.
	_, err = normalizeMistralChatRequest(account, bytes.Replace(body, []byte(`"extra":9007199254740993}]}`), []byte(`"extra":9007199254740992}]}`), 1))
	require.ErrorIs(t, err, errMistralContent)
	_, err = normalizeMistralChatRequest(account, []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"different"}],"mistral_content":[{"type":"text","text":"ok"}]}]}`))
	require.ErrorIs(t, err, errMistralContent)
}

func TestMistralReviewNestedEffort(t *testing.T) {
	for _, tc := range []struct{ name, fields, model, want string }{
		{"nested none", `"reasoning":{"effort":"none"},"reasoning_effort":"none"`, "zai-glm-5-3", "low"},
		{"nested wins", `"reasoning":{"effort":"high"},"reasoning_effort":"none"`, "zai-glm-5-3", "high"},
		{"nested only", `"reasoning":{"effort":"xhigh"}`, "zai-glm-5-3", "max"},
		{"flat", `"reasoning_effort":"minimal"`, "zai-glm-5-3", "low"},
		{"missing", `"metadata":{}`, "zai-glm-5-3", ""},
		{"other model", `"reasoning":{"effort":"none"}`, "mistral-small-latest", "none"},
		{"blank nested fallback", `"reasoning":{"effort":" "},"reasoning_effort":"medium"`, "zai-glm-5-3", "high"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":"alias",%s,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, tc.fields, stream))
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
				upstream := &chatRoleContractUpstream{}
				account := rawChatCompletionsTestAccount()
				account.Platform = PlatformMistral
				account.Credentials["model_mapping"] = map[string]any{"alias": tc.model}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				result, err := svc.forwardAsRawChatCompletions(context.Background(), c, account, body, "")
				require.NoError(t, err)
				require.Equal(t, tc.want, gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
				require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning").Exists())
				if tc.want == "" {
					require.Nil(t, result.ReasoningEffort)
				} else {
					require.NotNil(t, result.ReasoningEffort)
					require.Equal(t, tc.want, *result.ReasoningEffort)
				}
			})
		}
	}
	body := []byte(`{"model":"zai-glm-5-3","reasoning":{"effort":"none"},"reasoning_effort":"high","messages":[]}`)
	out, err := normalizeMistralChatRequest(&Account{Platform: PlatformOpenAI}, body)
	require.NoError(t, err)
	require.Equal(t, body, out)
}
