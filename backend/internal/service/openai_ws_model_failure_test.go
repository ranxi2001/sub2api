//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const issue907Error = `{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"model_not_found","message":"The 'gpt-5.4' model is not supported when using Codex with a ChatGPT account."}}`
const issue907Failed = `{"type":"response.failed","response":{"status":"failed","error":{"type":"invalid_request_error","code":"model_not_found","message":"The 'gpt-5.4' model is not supported when using Codex with a ChatGPT account."}}}`
const issue907Complete = `{"type":"response.completed","response":{"id":"resp_fixture907","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":0}}}`

type issue907CanceledRepo struct {
	modelNotFoundAccountRepoStub
	stateContextErr error
}

func (r *issue907CanceledRepo) SetModelRateLimit(ctx context.Context, id int64, model string, until time.Time, reason ...string) error {
	r.stateContextErr = ctx.Err()
	return r.modelNotFoundAccountRepoStub.SetModelRateLimit(ctx, id, model, until, reason...)
}

func TestIssue907CancellationStillPersistsModelCooldown(t *testing.T) {
	repo := &issue907CanceledRepo{}
	s := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.handleOpenAIWSErrorEventTransientFailure(ctx, wsSSETestAccount(), "gpt-5.4", nil, []byte(issue907Error))
	require.NoError(t, repo.stateContextErr, "the state write must receive an uncanceled, bounded context")
	require.Len(t, repo.modelRateLimitCalls, 1)
}

func issue907AssertCooldown(t *testing.T, s *OpenAIGatewayService, repo *modelNotFoundAccountRepoStub, account *Account, model string) {
	t.Helper()
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, account.ID, call.accountID)
	require.Equal(t, model, call.scope)
	require.Equal(t, upstreamCodexPlanGatedModelReason, call.reason)
	require.WithinDuration(t, time.Now().Add(30*time.Minute), call.resetAt, 5*time.Second)
	require.Zero(t, repo.tempCalls)
	require.False(t, s.isOpenAIAccountRuntimeBlocked(account))
	// Model the repository snapshot returned to the next scheduler lookup.
	copy := *account
	setAccountModelRateLimitSnapshot(&copy, model, call.resetAt, call.reason, time.Now())
	require.False(t, copy.IsSchedulableForModelWithContext(context.Background(), model))
	require.True(t, copy.IsSchedulableForModelWithContext(context.Background(), "gpt-5.6"))
}

func TestIssue907WSModelFailurePolicy(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "response_failed"}[terminal], func(t *testing.T) {
			repo := &modelNotFoundAccountRepoStub{}
			s := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo}}
			a := wsSSETestAccount()
			a.Credentials["model_mapping"] = map[string]any{"public": "gpt-5.4", "gpt-5.4": "gpt-5.6"}
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // state updates must survive the client disconnect
			if terminal {
				require.True(t, s.handleOpenAIWSFailureAccountSideEffects(ctx, a, "gpt-5.4", nil, []byte(issue907Failed)))
			} else {
				s.handleOpenAIWSErrorEventTransientFailure(ctx, a, "gpt-5.4", nil, []byte(issue907Error))
			}
			require.Len(t, repo.modelRateLimitCalls, 1)
			require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].scope, "do not apply the account mapping twice")
		})
	}
	for _, payload := range []string{
		`{"type":"error","status":400,"error":{"code":"invalid_request_error","message":"bad input"}}`,
		`{"type":"error","status":400,"error":{"code":"invalid_encrypted_content","message":"invalid history"}}`,
		`{"type":"response.completed","response":{"error":{"code":"model_not_found"}}}`,
		strings.Replace(issue907Error, `"status":400`, `"status":200`, 1),
	} {
		require.Zero(t, openAIWSModelFailureStatus(wsSSETestAccount(), []byte(payload)))
	}

	t.Run("configured_status_policy", func(t *testing.T) {
		repo := &modelNotFoundAccountRepoStub{}
		s := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo}}
		a := wsSSETestAccount()
		a.Type = AccountTypeAPIKey
		a.Credentials["custom_error_codes_enabled"] = true
		a.Credentials["custom_error_codes"] = []any{float64(429)}
		payload := []byte(`{"type":"error","status":404,"error":{"code":"model_not_found","message":"model not found"}}`)
		s.handleOpenAIWSErrorEventTransientFailure(context.Background(), a, "gpt-5.4", nil, payload)
		require.Empty(t, repo.modelRateLimitCalls)
	})
}

func TestIssue907WSModelReplaySafety(t *testing.T) {
	anchorCtx := context.WithValue(context.Background(), codexWSAnchorContextKey{}, &codexWSAnchorTurn{})
	require.False(t, openAIWSModelFailureCanReplay(anchorCtx, []byte(`{"input":"hello"}`)), "private Astra anchors are never a migratable first request")
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"input":"hello"}`, true},
		{`{"generate":false,"input":[]}`, true},
		{`{"input":[{"role":"user","content":"hello"}]}`, true},
		{`{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`, true},
		{`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.invalid/fixture.png"}]}]}`, false},
		{`{"input":[{"role":"tool","content":"result"}]}`, false},
		{`{"input":"hello","previous_response_id":"resp_old"}`, false},
		{`{"input":"hello","conversation":"conv_old"}`, false},
		{`{"input":[{"role":"assistant","content":"old output"}]}`, false},
		{`{"input":[{"type":"function_call_output","call_id":"call_fixture","output":"done"}]}`, false},
		{`{"input":[{"type":"agent_message","content":"history"}]}`, false},
		{`{"input":[{"type":"reasoning","encrypted_content":"synthetic"}]}`, false},
		{`{"input":[{"type":"item_reference","id":"item_fixture"}]}`, false},
		{`{"input":"first","input":"second"}`, false},
	} {
		raw := []byte(tc.body)
		require.Equal(t, tc.want, openAIWSModelFailureCanReplay(context.Background(), raw), tc.body)
		require.Equal(t, tc.body, string(raw), "the replay decision must not rewrite history")
	}
}

func TestIssue907ModelRejectionDoesNotPenalizeOtherModels(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	limiter, redis := qualitySignalTestLimiter(t)
	limiter.accountRepo = &modelNotFoundAccountRepoStub{}
	s := &OpenAIGatewayService{rateLimitService: limiter}
	s.settingService = NewSettingService(&openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{openAIAdvancedSchedulerSettingKey: "true"}}, nil)
	limiter.settingService = s.settingService
	s.initOpenAIAccountScheduler()
	a := wsSSETestAccount()
	// Seed a healthy account score, which is shared by every model on A.
	s.openaiAccountStats.reportAt(a.ID, true, nil, time.Now())
	before, _, _ := s.openaiAccountStats.snapshot(a.ID)
	for _, payload := range []string{issue907Error, issue907Failed} {
		s.handleOpenAIWSFailureAccountSideEffects(context.Background(), a, "gpt-5.4", nil, []byte(payload))
	}
	require.False(t, redis.Exists(quality5xxPendingKey), "a model 400 must not schedule any 5xx quality plan")
	for _, observed := range []error{ErrOpenAIWSModelUnavailable, s.newOpenAIWSModelFailoverError(a, []byte(issue907Error))} {
		for range 10 {
			s.ReportOpenAIAccountScheduleResult(a, "gpt-5.4", false, nil, observed)
		}
	}
	after, _, _ := s.openaiAccountStats.snapshot(a.ID)
	require.Equal(t, before, after, "model X rejection must leave the score used by model Y unchanged")
	require.False(t, s.newOpenAIWSModelFailoverError(a, []byte(issue907Error)).ShouldReportAccountScheduleFailure())
	s.ReportOpenAIAccountScheduleResult(a, "gpt-5.6", false, nil, errors.New("fixture network failure"))
	control, _, _ := s.openaiAccountStats.snapshot(a.ID)
	require.Greater(t, control, after, "the enabled account scorer must still observe ordinary failures")
	// Control: ordinary 5xx still reaches the queue.
	s.handleOpenAIWSFailureAccountSideEffects(context.Background(), a, "gpt-5.6", nil,
		[]byte(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"fixture"}}}`))
	require.True(t, redis.Exists(quality5xxPendingKey))
}

func TestIssue907HTTPToWSModelFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, prefix, input, failure, turnState string
		failover, prewarm                       bool
	}{
		{name: "fresh_request", failover: true},
		{name: "turn_state", turnState: "fixture-old-turn-state"},
		{name: "metadata_commits_existing_boundary", prefix: issue776RateLimits},
		{name: "after_output", prefix: `{"type":"response.output_text.delta","delta":"fixture"}`},
		{name: "opaque_history", input: `[{"type":"reasoning","encrypted_content":"synthetic"}]`},
		{name: "failed_terminal", failure: issue907Failed},
		{name: "generate_prewarm_error", prewarm: true, failover: true},
		{name: "generate_prewarm_failed", prewarm: true, failover: true, failure: issue907Failed},
		{name: "generate_prewarm_opaque", prewarm: true, input: `[{"type":"reasoning","encrypted_content":"synthetic"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := tc.failure
			if failure == "" {
				failure = issue907Error
			}
			conn := &openAIWSCaptureConn{}
			if tc.prefix != "" {
				conn.events = append(conn.events, []byte(tc.prefix))
			}
			conn.events = append(conn.events, []byte(failure))
			dialer := &wsSSETestDialer{conn: conn}
			httpPeer := &httpUpstreamRecorder{}
			s := wsSSETestService(t, dialer, httpPeer)
			s.cfg.Gateway.OpenAIWS.PrewarmGenerateEnabled = tc.prewarm
			repo := &modelNotFoundAccountRepoStub{}
			s.rateLimitService = &RateLimitService{accountRepo: repo}
			a := wsSSETestAccount()
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set(openAIWSTurnStateHeader, tc.turnState)
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			input := tc.input
			if input == "" {
				input = `[{"role":"user","content":"hello"}]`
			}
			body := []byte(`{"model":"gpt-5.4","stream":true,"instructions":"fixture","input":` + input + `}`)
			_, err := s.Forward(context.Background(), c, a, body)
			var failover *UpstreamFailoverError
			require.Equal(t, tc.failover, errors.As(err, &failover))
			if tc.failover {
				require.Equal(t, 400, failover.StatusCode)
				require.False(t, failover.RetryableOnSameAccount)
				require.False(t, c.Writer.Written())
			} else {
				require.Contains(t, w.Body.String(), "model_not_found")
				require.ErrorIs(t, err, ErrOpenAIWSModelUnavailable, "the outer handler must retain model-scoped attribution")
			}
			issue907AssertCooldown(t, s, repo, a, "gpt-5.4")
			require.EqualValues(t, 1, dialer.calls.Load())
			require.Nil(t, httpPeer.lastReq, "do not retry a submitted WS request over HTTP")
			conn.mu.Lock()
			require.Len(t, conn.writes, 1)
			if tc.prewarm {
				require.Equal(t, false, conn.writes[0]["generate"])
			}
			conn.mu.Unlock()
		})
	}
}

func TestIssue907LegacyWSBufferedProgressDoesNotAllowReplay(t *testing.T) {
	for _, progress := range []string{
		`{"type":"response.created","response":{"id":"resp_fixture907"}}`,
		`{"type":"response.output_item.done","item":{"type":"web_search_call","id":"fixture_search","status":"completed"}}`,
		`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"fixture_call","name":"lookup"}}`,
	} {
		t.Run(progress, func(t *testing.T) {
			conn := &openAIWSCaptureConn{events: [][]byte{[]byte(progress), []byte(issue907Error)}}
			s := wsSSETestService(t, &wsSSETestDialer{conn: conn}, &httpUpstreamRecorder{})
			repo := &modelNotFoundAccountRepoStub{}
			s.rateLimitService = &RateLimitService{accountRepo: repo}
			a := wsSSETestAccount()
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			body := map[string]any{"model": "gpt-5.4", "stream": true, "input": "hello"}
			_, err := s.forwardOpenAIWSV2(context.Background(), c, a, body, "", "", "fixture-token",
				NewOpenAIWSProtocolResolver(s.cfg).Resolve(a), true, true, "gpt-5.4", "gpt-5.4", time.Now(), 1, "", new(bool))
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover), "buffering progress is not proof that replay is safe")
			require.Contains(t, w.Body.String(), "model_not_found")
			issue907AssertCooldown(t, s, repo, a, "gpt-5.4")
		})
	}
}

func TestIssue907NativeWSModelFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, input, turnState, failure string
		prewarm, passthrough, failover  bool
	}{
		{name: "fresh", input: `"hello"`, failover: true},
		{name: "turn_state", input: `"hello"`, turnState: "fixture-old-turn-state"},
		{name: "encrypted_first_turn", input: `[{"type":"reasoning","encrypted_content":"synthetic"}]`},
		{name: "prewarm_then_task", input: `"real task"`, prewarm: true},
		{name: "passthrough", input: `"hello"`, passthrough: true},
		{name: "failed_after_prewarm", input: `[{"type":"reasoning","encrypted_content":"synthetic"},{"type":"function_call","call_id":"fixture","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"fixture","output":"done"}]`, prewarm: true, failure: issue907Failed},
		{name: "passthrough_failed", input: `"hello"`, passthrough: true, failure: issue907Failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &openAIWSCaptureConn{}
			if tc.prewarm {
				upstream.events = append(upstream.events, []byte(issue907Complete))
			}
			failure := tc.failure
			if failure == "" {
				failure = issue907Error
			}
			upstream.events = append(upstream.events, []byte(failure))
			s := wsSSETestService(t, &wsSSETestDialer{conn: upstream}, &httpUpstreamRecorder{})
			repo := &modelNotFoundAccountRepoStub{}
			s.rateLimitService = &RateLimitService{accountRepo: repo}
			a := wsSSETestAccount()
			s.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			a.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
			if tc.passthrough {
				a.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModePassthrough
				s.openaiWSPassthroughDialer = &openAIWSCaptureDialer{conn: upstream}
			}
			done := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					done <- err
					return
				}
				defer func() { _ = conn.CloseNow() }()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r.Clone(r.Context())
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				_, first, err := conn.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				forwardErr := s.ProxyResponsesWebSocketFromClient(ctx, c, conn, a, "fixture-token", first, nil)
				var closeErr *OpenAIWSClientCloseError
				if errors.As(forwardErr, &closeErr) {
					_ = conn.Close(closeErr.StatusCode(), closeErr.Reason())
				}
				done <- forwardErr
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &coderws.DialOptions{
				HTTPHeader: http.Header{openAIWSTurnStateHeader: {tc.turnState}},
			})
			require.NoError(t, err)
			defer func() { _ = client.CloseNow() }()
			if tc.prewarm {
				require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","generate":false,"input":[]}`)))
				_, response, readErr := client.Read(ctx)
				require.NoError(t, readErr)
				require.Contains(t, string(response), "response.completed")
			}
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","input":`+tc.input+`}`)))
			_, response, readErr := client.Read(ctx)
			if tc.failover {
				require.Error(t, readErr)
			} else {
				require.NoError(t, readErr)
				require.JSONEq(t, failure, string(response))
				_, _, closeErr := client.Read(ctx)
				require.Equal(t, coderws.StatusTryAgainLater, coderws.CloseStatus(closeErr))
			}
			_ = client.CloseNow()
			select {
			case forwardErr := <-done:
				var failover *UpstreamFailoverError
				require.Equal(t, tc.failover, errors.As(forwardErr, &failover))
				if !tc.failover {
					require.ErrorIs(t, forwardErr, ErrOpenAIWSModelUnavailable)
				}
			case <-ctx.Done():
				t.Fatal("native forwarding did not stop")
			}
			issue907AssertCooldown(t, s, repo, a, "gpt-5.4")
			upstream.mu.Lock()
			wantWrites := 1
			if tc.prewarm {
				wantWrites = 2
			}
			require.Len(t, upstream.writes, wantWrites, "never replay the original prewarm or strip history")
			actualInput, marshalErr := json.Marshal(upstream.writes[len(upstream.writes)-1]["input"])
			require.NoError(t, marshalErr)
			require.JSONEq(t, tc.input, string(actualInput), "the current task history must reach upstream intact")
			upstream.mu.Unlock()
		})
	}
}
