package qqbot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type chatResponseError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e chatResponseError) Error() string { return e.Message }

func incompleteChatResponse(message string) error {
	return chatResponseError{Code: "incomplete_response", Message: message, Retryable: true}
}

// Only known transient API errors are retried. Policy, quota, length and
// unknown failures retain the original single-attempt behavior.
func chatCompletionFailure(status string, rawError, details json.RawMessage) error {
	var failure struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}
	var incomplete struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(rawError, &failure)
	_ = json.Unmarshal(details, &incomplete)
	code := failure.Code
	if code == "" {
		code = failure.Type
	}
	if incomplete.Reason != "" {
		code = incomplete.Reason
	}
	switch code {
	case "invalid_prompt", "invalid_request", "bad_request", "unauthorized", "forbidden", "insufficient_quota", "payment_required":
		return chatResponseError{Code: "model_policy", Message: "模型请求未被接受"}
	case "max_output_tokens", "length":
		return chatResponseError{Code: "output_limit", Message: "模型输出达到长度限制"}
	case "content_filter", "content_policy_violation":
		return chatResponseError{Code: "content_filter", Message: "模型回复被安全策略拦截"}
	case "server_error", "internal_error", "internal_server_error", "overloaded_error", "service_unavailable", "rate_limit_exceeded", "rate_limit_error", "timeout", "request_timeout":
		return chatResponseError{Code: "upstream_transient", Message: "模型暂时未完成回复", Retryable: true}
	}
	if code == "" && status == "incomplete" {
		return incompleteChatResponse("模型未完成回复")
	}
	if code == "" && status == "failed" {
		return chatResponseError{Code: "upstream_failed", Message: "模型暂时未完成回复", Retryable: true}
	}
	return chatResponseError{Code: "model_failed", Message: "模型未完成回复"}
}

type chatRetryPolicy struct {
	maxAttempts     int
	maxElapsed      time.Duration
	maxAttempt      time.Duration
	deadlineReserve time.Duration
	delays          []time.Duration
	maxRetryAfter   time.Duration
}

func (s *Service) siteChatTurn(ctx context.Context, c Config, messages []Message) (chatTurnResult, error) {
	return retryChatTurn(ctx, func(attemptCtx context.Context) (chatTurnResult, error) {
		return s.siteChatTurnOnce(attemptCtx, c, messages)
	}, chatRetryPolicy{
		maxAttempts: 3, maxElapsed: 180 * time.Second, maxAttempt: 50 * time.Second,
		deadlineReserve: 10 * time.Second, delays: []time.Duration{time.Second, 2 * time.Second},
		maxRetryAfter: 15 * time.Second,
	})
}

func retryChatTurn(ctx context.Context, attempt func(context.Context) (chatTurnResult, error), policy chatRetryPolicy) (chatTurnResult, error) {
	deadline := time.Now().Add(policy.maxElapsed)
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Add(-policy.deadlineReserve).Before(deadline) {
		deadline = parentDeadline.Add(-policy.deadlineReserve)
	}
	retryCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	execution := ChatExecution{UsageComplete: true}
	var input, output int64
	finish := func(turn chatTurnResult, err error) (chatTurnResult, error) {
		if err != nil {
			// A rejected or partial response can never leak an action to the caller.
			turn = chatTurnResult{}
		}
		turn.InputTokens, turn.OutputTokens, turn.ChatExecution = input, output, execution
		return turn, err
	}
	var lastErr error
	for n := 0; n < policy.maxAttempts; n++ {
		if err := retryCtx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			}
			return finish(chatTurnResult{}, lastErr)
		}
		// Share the remaining budget across outstanding attempts so one slow
		// request cannot consume all time reserved for retry and final delivery.
		remaining := time.Until(deadline)
		var waits time.Duration
		for i := n; i < policy.maxAttempts-1 && i < len(policy.delays); i++ {
			waits += policy.delays[i]
		}
		if remaining > waits {
			remaining -= waits
		}
		attemptBudget := remaining / time.Duration(policy.maxAttempts-n)
		if attemptBudget > policy.maxAttempt {
			attemptBudget = policy.maxAttempt
		}
		attemptCtx, attemptCancel := context.WithTimeout(retryCtx, attemptBudget)
		turn, err := attempt(attemptCtx)
		attemptCancel()
		execution.Attempts++
		execution.Retries = execution.Attempts - 1
		execution.UsageComplete = execution.UsageComplete && turn.ChatExecution.UsageComplete
		input += turn.InputTokens
		output += turn.OutputTokens
		if err == nil {
			return finish(turn, nil)
		}
		lastErr = err
		code, retryable, retryAfter := chatRetryDecision(err, time.Now())
		execution.LastErrorCode = code
		if !retryable || n+1 >= policy.maxAttempts || retryCtx.Err() != nil {
			return finish(chatTurnResult{}, err)
		}
		if retryAfter > policy.maxRetryAfter {
			return finish(chatTurnResult{}, err)
		}
		var delay time.Duration
		if n < len(policy.delays) {
			delay = policy.delays[n]
		}
		if retryAfter > delay {
			delay = retryAfter
		}
		if delay >= time.Until(deadline) {
			return finish(chatTurnResult{}, err)
		}
		timer := time.NewTimer(delay)
		select {
		case <-retryCtx.Done():
			timer.Stop()
			return finish(chatTurnResult{}, err)
		case <-timer.C:
		}
	}
	return finish(chatTurnResult{}, lastErr)
}

func chatRetryDecision(err error, now time.Time) (string, bool, time.Duration) {
	if errors.Is(err, context.Canceled) {
		return "canceled", false, 0
	}
	var failure *requestFailure
	if errors.As(err, &failure) && failure.StatusCode != 0 {
		status := failure.StatusCode
		retry := status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests || status >= 500 && status <= 599
		return fmt.Sprintf("http_%d", status), retry, chatRetryAfter(failure.RetryAfter, now)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", true, 0
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return "timeout", true, 0
		}
		return "transport", true, 0
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "incomplete_response", true, 0
	}
	var responseError chatResponseError
	if errors.As(err, &responseError) {
		return responseError.Code, responseError.Retryable, 0
	}
	if errors.Is(err, errChatActionMissing) {
		return "missing_action", true, 0
	}
	return "invalid_response", false, 0
}

func chatRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		// Saturate before converting untrusted seconds to a duration.
		if seconds > 86400 {
			return 24 * time.Hour
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}

// Count a usage snapshot once per attempt, including failed attempts. A missing
// side stays unknown even when the other token count is available. Zero is a
// valid reported value, so presence is checked independently from the number.
func chatResponseUsage(raw []byte, contentType string) (input, output int64, complete bool) {
	if strings.Contains(contentType, "text/event-stream") {
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			i, o, ok := chatResponseUsage([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), "application/json")
			if ok {
				input, output, complete = i, o, true
			} else if !complete {
				if i > input {
					input = i
				}
				if o > output {
					output = o
				}
			}
		}
		return
	}
	var envelope struct {
		Usage *struct {
			Input      *int64 `json:"input_tokens"`
			Output     *int64 `json:"output_tokens"`
			Prompt     *int64 `json:"prompt_tokens"`
			Completion *int64 `json:"completion_tokens"`
		} `json:"usage"`
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return
	}
	if envelope.Usage == nil {
		if len(envelope.Response) > 0 {
			return chatResponseUsage(envelope.Response, "application/json")
		}
		return
	}
	u := envelope.Usage
	in, out := u.Input, u.Output
	if in == nil {
		in = u.Prompt
	}
	if out == nil {
		out = u.Completion
	}
	if in != nil && *in >= 0 {
		input = *in
	}
	if out != nil && *out >= 0 {
		output = *out
	}
	return input, output, in != nil && out != nil && *in >= 0 && *out >= 0
}
