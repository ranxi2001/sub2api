package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func fastChatRetryPolicy() chatRetryPolicy {
	return chatRetryPolicy{maxAttempts: 3, maxElapsed: time.Second, maxAttempt: 100 * time.Millisecond, maxRetryAfter: 15 * time.Second}
}

func TestChatRetryClassifiesStatusAndPolicyErrors(t *testing.T) {
	for _, status := range []int{400, 401, 402, 403, 404, 408, 422, 425, 429, 500, 502, 503, 504} {
		_, retry, _ := chatRetryDecision(&requestFailure{StatusCode: status}, time.Now())
		require.Equal(t, status == 408 || status == 425 || status == 429 || status >= 500, retry, "HTTP %d", status)
	}
	for _, tc := range []struct{ reason, code string }{{"length", "output_limit"}, {"max_output_tokens", "output_limit"}, {"content_filter", "content_filter"}} {
		details, err := json.Marshal(map[string]string{"reason": tc.reason})
		require.NoError(t, err)
		code, retry, _ := chatRetryDecision(chatCompletionFailure("incomplete", nil, details), time.Now())
		require.Equal(t, tc.code, code)
		require.False(t, retry)
	}
	for _, code := range []string{"invalid_prompt", "insufficient_quota", "content_policy_violation", "unrecognized_error"} {
		raw, err := json.Marshal(map[string]string{"code": code})
		require.NoError(t, err)
		_, retry, _ := chatRetryDecision(chatCompletionFailure("failed", raw, nil), time.Now())
		require.False(t, retry, code)
	}
}

func TestChatRetryAccumulatesUsageAndDropsRejectedActions(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(boolText(complete), func(t *testing.T) {
			calls := 0
			turn, err := retryChatTurn(context.Background(), func(context.Context) (chatTurnResult, error) {
				calls++
				result := chatTurnResult{InputTokens: int64(calls), OutputTokens: 2, ChatExecution: ChatExecution{UsageComplete: complete || calls > 1}}
				if calls < 3 {
					result.ImagePrompt = "never execute rejected tool"
					return result, incompleteChatResponse("partial")
				}
				result.Text = "成功"
				return result, nil
			}, fastChatRetryPolicy())
			require.NoError(t, err)
			require.Equal(t, "成功", turn.Text)
			require.Empty(t, turn.ImagePrompt)
			require.EqualValues(t, 6, turn.InputTokens)
			require.EqualValues(t, 6, turn.OutputTokens)
			require.Equal(t, ChatExecution{Attempts: 3, Retries: 2, UsageComplete: complete, LastErrorCode: "incomplete_response"}, turn.ChatExecution)
		})
	}
	turn, err := retryChatTurn(context.Background(), func(context.Context) (chatTurnResult, error) {
		return chatTurnResult{ImagePrompt: "invalid", InputTokens: 4}, errors.New("invalid tool parameters")
	}, fastChatRetryPolicy())
	require.Error(t, err)
	require.Empty(t, turn.ImagePrompt)
	require.Equal(t, 1, turn.ChatExecution.Attempts)
	require.EqualValues(t, 4, turn.InputTokens)
}

func TestChatRetryTimeoutsLeaveBudgetForAnotherAttempt(t *testing.T) {
	calls := 0
	policy := fastChatRetryPolicy()
	policy.maxAttempt = 5 * time.Millisecond
	turn, err := retryChatTurn(context.Background(), func(ctx context.Context) (chatTurnResult, error) {
		calls++
		if calls == 1 {
			<-ctx.Done()
			return chatTurnResult{}, ctx.Err()
		}
		return chatTurnResult{Text: "recovered", ChatExecution: ChatExecution{UsageComplete: true}}, nil
	}, policy)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, "timeout", turn.ChatExecution.LastErrorCode)
	require.False(t, turn.ChatExecution.UsageComplete)
}

func TestChatRetryHonorsRetryAfterAndCancellation(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	require.Equal(t, 3*time.Second, chatRetryAfter("3", now))
	require.Equal(t, 8*time.Second, chatRetryAfter(now.Add(8*time.Second).UTC().Format(http.TimeFormat), now))
	require.Zero(t, chatRetryAfter("-1", now))
	require.Zero(t, chatRetryAfter("bad", now))
	require.Greater(t, chatRetryAfter("9223372036854775807", now), 15*time.Second)
	for _, after := range []string{"16", "2", time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)} {
		turn, err := retryChatTurn(context.Background(), func(context.Context) (chatTurnResult, error) {
			return chatTurnResult{}, &requestFailure{StatusCode: 429, RetryAfter: after}
		}, fastChatRetryPolicy())
		require.Error(t, err)
		require.Equal(t, 1, turn.ChatExecution.Attempts)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	turn, err := retryChatTurn(ctx, func(context.Context) (chatTurnResult, error) {
		calls++
		cancel()
		return chatTurnResult{}, io.ErrUnexpectedEOF
	}, fastChatRetryPolicy())
	require.Error(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, 1, turn.ChatExecution.Attempts)
	turn, err = retryChatTurn(ctx, func(context.Context) (chatTurnResult, error) {
		t.Fatal("canceled context must not start an attempt")
		return chatTurnResult{}, nil
	}, fastChatRetryPolicy())
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, turn.ChatExecution.Attempts)
}

func TestChatResponseUsagePresenceAndSSESnapshots(t *testing.T) {
	for _, tc := range []struct {
		raw           string
		input, output int64
		complete      bool
	}{
		{`{"usage":{"input_tokens":0,"output_tokens":0}}`, 0, 0, true},
		{`{"usage":{"prompt_tokens":5,"completion_tokens":2}}`, 5, 2, true},
		{`{"usage":{"input_tokens":5}}`, 5, 0, false},
		{`{"usage":{"output_tokens":2}}`, 0, 2, false},
		{`{"usage":null}`, 0, 0, false},
		{`{"usage":{"input_tokens":-1,"output_tokens":2}}`, 0, 2, false},
		{`{}`, 0, 0, false},
	} {
		in, out, complete := chatResponseUsage([]byte(tc.raw), "application/json")
		require.Equal(t, tc.input, in)
		require.Equal(t, tc.output, out)
		require.Equal(t, tc.complete, complete)
	}
	raw := "data: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\ndata: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\ndata: [DONE]\n"
	in, out, complete := chatResponseUsage([]byte(raw), "text/event-stream")
	require.EqualValues(t, 5, in)
	require.EqualValues(t, 3, out)
	require.True(t, complete)
}

func TestSiteChatPermanentAndIncompleteResults(t *testing.T) {
	for name, raw := range map[string]string{
		"length": `{"choices":[{"finish_reason":"length","message":{"content":"partial"}}],"usage":{"prompt_tokens":9,"completion_tokens":3}}`,
		"filter": `{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"usage":{"input_tokens":9,"output_tokens":3}}`,
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; _, _ = w.Write([]byte(raw)) }))
			defer server.Close()
			c := DefaultConfig()
			c.BaseURL = server.URL
			turn, err := New(nil, nil, false, nil).siteChatTurn(context.Background(), c, []Message{{Role: "user", Content: "你好"}})
			require.Error(t, err)
			require.Equal(t, 1, calls)
			require.Empty(t, turn.Text)
			require.True(t, turn.ChatExecution.UsageComplete)
			require.EqualValues(t, 9, turn.InputTokens)
		})
	}
}

func TestSiteChatRetriesOnlyTransientSSEErrors(t *testing.T) {
	for _, tc := range []struct {
		code     string
		attempts int
	}{
		{"server_error", 3}, {"rate_limit_exceeded", 3}, {"content_filter", 1}, {"insufficient_quota", 1},
	} {
		t.Run(tc.code, func(t *testing.T) {
			event, err := json.Marshal(map[string]string{"type": "error", "code": tc.code, "message": "sensitive upstream message must not leak"})
			require.NoError(t, err)
			turn, err := retryChatTurn(context.Background(), func(context.Context) (chatTurnResult, error) {
				return parseChatTurn(append(append([]byte("data: "), event...), '\n', '\n'), "text/event-stream")
			}, fastChatRetryPolicy())
			require.Error(t, err)
			require.NotContains(t, err.Error(), "sensitive")
			require.Equal(t, tc.attempts, turn.ChatExecution.Attempts)
		})
	}
}
