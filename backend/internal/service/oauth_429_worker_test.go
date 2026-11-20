package service

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOAuth429VerdictsRequireCompleteExactTarget(t *testing.T) {
	for _, tc := range []struct {
		name, answer, status, message, model, want string
		cancel                                     bool
	}{
		{name: "wrong", answer: "23", want: "incorrect"},
		{name: "pass", answer: "21", want: "correct"},
		{name: "whitespace", answer: " 21\n", want: "correct"},
		{name: "leading zeros", answer: "021", want: "correct"},
		{name: "empty", want: "inconclusive"},
		{name: "explanation", answer: "answer: 23", want: "inconclusive"},
		{name: "refusal", answer: "I cannot answer", want: "inconclusive"},
		{name: "429", answer: "23", status: "failed", message: "API returned 429", want: "inconclusive"},
		{name: "timeout", answer: "23", status: "failed", message: "timeout", want: "inconclusive"},
		{name: "auth", answer: "23", status: "failed", message: "API returned 401", want: "inconclusive"},
		{name: "balance", answer: "23", status: "failed", message: "API returned 402", want: "inconclusive"},
		{name: "truncated", answer: "23", message: "stream incomplete", want: "inconclusive"},
		{name: "other model", answer: "23", model: "gpt-6-luna", want: "inconclusive"},
		{name: "cancelled", answer: "23", cancel: true, want: "inconclusive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, model := tc.status, tc.model
			if status == "" {
				status = "success"
			}
			if model == "" {
				model = "gpt-6-astra"
			}
			result := &ScheduledTestResult{Status: status, ResponseText: tc.answer, ErrorMessage: tc.message, PelicanConfig: &PelicanTestConfig{ModelID: model}}
			var ctxErr error
			if tc.cancel {
				ctxErr = context.Canceled
			}
			require.Equal(t, tc.want, oauth429Verdict(result, nil, ctxErr, "gpt-6-astra"))
		})
	}
	require.Equal(t, "inconclusive", oauth429Verdict(nil, nil, nil, "gpt-6-astra"))
	require.Equal(t, "inconclusive", oauth429Verdict(&ScheduledTestResult{}, errors.New("transport"), nil, "gpt-6-astra"))
}

func TestOAuth429ExecutionRejectsOtherAccountOrModel(t *testing.T) {
	ctx := context.WithValue(t.Context(), oauth429ExecutionKey{}, oauth429Execution{AccountID: 7, Model: "gpt-6-astra"})
	for _, tc := range []struct {
		id    int64
		model string
		ok    bool
	}{{7, "gpt-6-astra", true}, {8, "gpt-6-astra", false}, {7, "gpt-6-luna", false}} {
		request := httptest.NewRequest("POST", "http://127.0.0.1/fixture", strings.NewReader(`{"model":"`+tc.model+`"}`)).WithContext(ctx)
		// httptest.NewRequest does not supply a reusable body; real adapters must.
		request.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(`{"model":"` + tc.model + `"}`)), nil
		}
		require.Equal(t, tc.ok, ValidateOAuth429Upstream(request, tc.id) == nil)
	}
}

func TestOAuth429RetainedNumericSummary(t *testing.T) {
	// Retained result 373436 has expected=21, numeric fragment=29, chars=2.
	// Recorded MD5 equals MD5("29"), so this exact short answer is recoverable.
	// This checks classifier behavior on that retained answer, not LLM accuracy,
	// and supplies no evidence to any new trigger.
	result := &ScheduledTestResult{Status: "success", ResponseText: "29", StartedAt: time.Now(), FinishedAt: time.Now(), PelicanConfig: &PelicanTestConfig{ModelID: "gpt-6-astra"}}
	require.Equal(t, "incorrect", oauth429Verdict(result, nil, nil, "gpt-6-astra"))
}
