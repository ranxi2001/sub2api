//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCialloFieldExamples(t *testing.T) {
	// Source labels and public examples: narutoFyy/sub2-fwq2
	// 48e1184175b1f693587971b03d66fb77b3fea46f, TestIsCialloReply.
	for _, answer := range []string{"Ciallo～(∠・ω< )⌒☆", "Ciallo～(∠・ω< )⌒★ 有什么我可以帮你的吗？", "ciallo~ (≧▽≦)", "Ciallo！(^_^)"} {
		require.True(t, cialloGreeting.MatchString(answer), answer)
	}
	for _, answer := range []string{"Ciallo! 👋 What can I help you with?", "Ciallo～ 👋 今天想聊点什么？", "Hello! How can I help? (∠・ω< )⌒☆", "Ciallo (hello) there", ""} {
		require.False(t, cialloGreeting.MatchString(answer), answer)
	}
}

func TestCialloOnlyScoresCompletedText(t *testing.T) {
	const content = "data: {\"type\":\"response.output_text.delta\",\"delta\":\" Ciallo～(∠・ω< )⌒☆ trailing text\"}\n\n"
	const complete = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
	for _, tc := range []struct {
		name, body, outcome, reason string
		status                      int
	}{
		{"match", content + complete, "matched", "", 200},
		{"empty", complete, "unassessable", "empty", 200},
		{"partial", content, "unassessable", "incomplete", 200},
		{"truncated", content + "data: {\"type\":\"response.done\",\"response\":{\"status\":\"incomplete\"}}\n\n", "unassessable", "incomplete", 200},
		{"bare_word", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Ciallo\"}\n\n" + complete, "unmatched", "", 200},
		{"limited", "private upstream details", "unassessable", "limited", 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{ID: 77, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "mock-only", "base_url": "https://example.com"}}
			upstream := &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(tc.status, tc.body)}}
			repo := &modelTraceRepo{account: account}
			svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, cfg: &config.Config{}}
			result, err := svc.RunCiallo(context.Background(), 77, "sample-model", "medium")
			require.NoError(t, err)
			require.Equal(t, tc.outcome, result.Outcome)
			require.Equal(t, tc.reason, result.Reason)
			require.NotContains(t, result.Greeting, "trailing text")
			require.Len(t, upstream.requests, 1)
			body, err := io.ReadAll(upstream.requests[0].Body)
			require.NoError(t, err)
			var payload map[string]any
			require.NoError(t, json.Unmarshal(body, &payload))
			require.Contains(t, string(body), `"text":"ciallo"`)
			require.NotContains(t, payload, "instructions")
			require.NotContains(t, payload, "tools")
			require.Zero(t, repo.mutations)
		})
	}
}
