package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOAuth429VerdictsRequireCompleteExactTarget(t *testing.T) {
	for _, tc := range []struct {
		name, answer, status, message, model, want string
		cancel, incomplete                         bool
	}{
		{name: "unfinished answer", answer: "23", incomplete: true, want: "inconclusive"},
		{name: "formatted pass", answer: "答案是21颗糖果。", want: "correct"},
		{name: "wrapped HTTP error", answer: "429", want: "inconclusive"},
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
			require.Equal(t, tc.want, oauth429Verdict(result, nil, ctxErr, "gpt-6-astra", !tc.incomplete))
		})
	}
	require.Equal(t, "inconclusive", oauth429Verdict(nil, nil, nil, "gpt-6-astra", true))
	require.Equal(t, "inconclusive", oauth429Verdict(&ScheduledTestResult{}, errors.New("transport"), nil, "gpt-6-astra", true))
}

func TestOAuth429ExecutionRejectsOtherAccountOrModel(t *testing.T) {
	ctx := context.WithValue(t.Context(), oauth429ExecutionKey{}, &oauth429Execution{AccountID: 7, Model: "gpt-6-astra"})
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
	require.Equal(t, "incorrect", oauth429Verdict(result, nil, nil, "gpt-6-astra", true))
}

func TestOAuth429ConstructionMetadataDoesNotDependOnGetBody(t *testing.T) {
	ctx := context.WithValue(t.Context(), ctxkey.Model, "alias-one")
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "metadata-fixture")
	body := []byte(`{"input":"` + strings.Repeat("x", (1<<20)+128) + `","model":"gpt-6-astra"}`)
	ctx = withOAuth429WireBody(ctx, body)
	require.Equal(t, "gpt-6-astra", ctx.Value(oauth429WireModelKey{}))
	require.Equal(t, "alias-one", ctx.Value(ctxkey.Model))
	require.Equal(t, "metadata-fixture", ctx.Value(ctxkey.ClientRequestID))
}

func TestOAuth429ActualHTTPBuildersKeepParsedModel(t *testing.T) {
	ctx := context.WithValue(t.Context(), ctxkey.Model, "alias-one")
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "builder-business")
	body := []byte(`{"input":"` + strings.Repeat("x", (1<<20)+128) + `","model":"gpt-6-astra","stream":true}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{}}
	builders := []struct {
		name  string
		build func() (*http.Request, error)
	}{
		{"responses", func() (*http.Request, error) {
			return svc.buildUpstreamRequest(ctx, c, account, body, "fixture", true, "", false)
		}},
		{"passthrough", func() (*http.Request, error) {
			return svc.buildUpstreamRequestOpenAIPassthrough(ctx, c, account, body, "fixture")
		}},
		{"native_anthropic", func() (*http.Request, error) {
			r, _, e := svc.buildNativeAnthropicUpstreamRequest(ctx, c, account, body, "fixture", "https://fixture.example/v1/messages")
			return r, e
		}},
	}
	for _, builder := range builders {
		t.Run(builder.name, func(t *testing.T) {
			req, err := builder.build()
			require.NoError(t, err)
			req.GetBody = nil
			require.Equal(t, "gpt-6-astra", req.Context().Value(oauth429WireModelKey{}))
			require.Equal(t, "alias-one", req.Context().Value(ctxkey.Model))
			require.Equal(t, "builder-business", req.Context().Value(ctxkey.ClientRequestID))
		})
	}
}
