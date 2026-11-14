package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func imageReviewTestJSON(t *testing.T, protocol string, review imageIntentReview) []byte {
	t.Helper()
	args, err := json.Marshal(review)
	require.NoError(t, err)
	return []byte(strings.ReplaceAll(string(imageToolTestJSON(t, protocol, string(args))), `"name":"generate_image"`, `"name":"review_image_intent"`))
}

func TestImageIntentReviewIsolatedFromPersonaAndHistory(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				key := "messages"
				if protocol == "responses" {
					key = "input"
				}
				messages, ok := body[key].([]any)
				require.True(t, ok)
				require.Len(t, messages, 2)
				instructions, ok := messages[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, imageIntentInstructions, instructions["content"])
				raw, _ := json.Marshal(body)
				require.NotContains(t, string(raw), "PERSONA_SECRET")
				require.NotContains(t, string(raw), "SITE_KNOWLEDGE_SHOULD_NOT_APPEAR")
				require.NotContains(t, string(raw), "data:image")
				require.Equal(t, "required", body["tool_choice"])
				require.Equal(t, false, body["parallel_tool_calls"])
				require.Len(t, body["tools"], 1)
				_, _ = w.Write(imageReviewTestJSON(t, protocol, imageIntentReview{Disposition: "text", Basis: "none", Text: "下午好！"}))
			}))
			defer server.Close()
			s := New(nil, nil, false, nil)
			c := DefaultConfig()
			c.BaseURL, c.ChatProtocol, c.SystemPrompt = server.URL, protocol, "PERSONA_SECRET"
			c.SiteKnowledge = SiteKnowledgeConfig{Enabled: true, Content: "SITE_KNOWLEDGE_SHOULD_NOT_APPEAR"}
			r, usage, err := s.reviewImageIntent(context.Background(), c, Event{}, "下午好🌹", Message{Images: []string{"data:image/png;base64,AAAA"}}, nil, nil, chatImageToolName, "自作主张画玫瑰", 0)
			require.NoError(t, err)
			require.Equal(t, "text", r.Disposition)
			require.EqualValues(t, 11, usage.InputTokens)
			require.Equal(t, 1, calls)
		})
	}
}

func TestImageIntentReviewParserRequiresCompleteValidTool(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		good := imageReviewTestJSON(t, protocol, imageIntentReview{Disposition: "execute", Basis: "explicit", Evidence: "画一只猫"})
		r, _, err := parseImageIntentReview(good, "application/json")
		require.NoError(t, err)
		require.Equal(t, "execute", r.Disposition)
		stream := imageToolTestStream(t, protocol, `{"disposition":"execute","basis":"explicit","evidence":"画一只猫","text":""}`)
		stream = []byte(strings.ReplaceAll(string(stream), chatImageToolName, imageReviewToolName))
		_, _, err = parseImageIntentReview(stream, "text/event-stream")
		require.NoError(t, err)
		for _, raw := range [][]byte{
			imageToolTestJSON(t, protocol, `{"prompt":"cat"}`),
			imageReviewTestJSON(t, protocol, imageIntentReview{Disposition: "maybe", Basis: "explicit"}),
			imageReviewTestJSON(t, protocol, imageIntentReview{Disposition: "text", Basis: "none"}),
			[]byte(strings.ReplaceAll(string(good), `"completed"`, `"incomplete"`)),
		} {
			if string(raw) == string(good) {
				continue
			}
			_, _, err = parseImageIntentReview(raw, "application/json")
			require.Error(t, err)
		}
	}
}
