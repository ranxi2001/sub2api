//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type issue907Repo struct {
	openAIWSFailoverHandlerAccountRepoStub
	mu            sync.Mutex
	writes        []int64
	canceledWrite bool
}

func (r *issue907Repo) ListSchedulableByPlatform(ctx context.Context, p string) ([]service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.openAIWSFailoverHandlerAccountRepoStub.ListSchedulableByPlatform(ctx, p)
}
func (r *issue907Repo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, p string) ([]service.Account, error) {
	return r.ListSchedulableByPlatform(ctx, p)
}
func (r *issue907Repo) ListSchedulableUngroupedByPlatform(ctx context.Context, p string) ([]service.Account, error) {
	return r.ListSchedulableByPlatform(ctx, p)
}
func (r *issue907Repo) GetByID(ctx context.Context, id int64) (*service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.openAIWSFailoverHandlerAccountRepoStub.GetByID(ctx, id)
}
func (r *issue907Repo) GetOpenAITurnAdmission(ctx context.Context, id int64) (*service.Account, *service.Account, error) {
	a, e := r.GetByID(ctx, id)
	return a, nil, e
}
func (r *issue907Repo) UpdateExtra(context.Context, int64, map[string]any) error { return nil }
func (r *issue907Repo) SetModelRateLimit(ctx context.Context, id int64, model string, until time.Time, reason ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.canceledWrite = r.canceledWrite || ctx.Err() != nil
	r.writes = append(r.writes, id)
	for i := range r.accounts {
		if r.accounts[i].ID != id {
			continue
		}
		extra := make(map[string]any, len(r.accounts[i].Extra)+1)
		for k, v := range r.accounts[i].Extra {
			extra[k] = v
		}
		entry := map[string]any{"rate_limit_reset_at": until.UTC().Format(time.RFC3339)}
		if len(reason) > 0 {
			entry["reason"] = reason[0]
		}
		extra["model_rate_limits"] = map[string]any{model: entry}
		r.accounts[i].Extra = extra
	}
	return nil
}
func (*issue907Repo) SetError(context.Context, int64, string) error {
	panic("model rejection must not disable the account")
}
func (*issue907Repo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	panic("model rejection must not ban all models")
}

func TestIssue907HandlerSwitchAndNextRequest(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		name := "A_to_B"
		if exhausted {
			name = "exhausted"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var hits []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				for {
					_, body, err := conn.Read(r.Context())
					if err != nil {
						return
					}
					id := r.Header.Get("Authorization")
					mu.Lock()
					hits = append(hits, id)
					mu.Unlock()
					response := `{"type":"response.completed","response":{"id":"resp_fixture907","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":0}}}`
					if (id == "Bearer fixture-A" || exhausted) && gjson.GetBytes(body, "model").String() == "gpt-5.4" {
						response = `{"type":"error","status":404,"error":{"code":"model_not_found","message":"model not found"}}`
					}
					if conn.Write(r.Context(), coderws.MessageText, []byte(response)) != nil {
						return
					}
				}
			}))
			defer upstream.Close()
			repo := &issue907Repo{}
			headers := http.Header{"Session_id": {"fixture907-session"}}
			h := newOpenAIWSPassthroughHandlerHarnessWithOptions(t, upstream.URL, headers, nil,
				func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, ar *service.AccountRepository) {
					cfg.Gateway.MaxAccountSwitches = 2
					a.ID = 1
					a.Priority = 0
					a.Credentials = map[string]any{"api_key": "fixture-A", "base_url": upstream.URL}
					a.Extra = map[string]any{"openai_apikey_responses_websockets_v2_mode": service.OpenAIWSIngressModeCtxPool}
					b := *a
					b.ID = 2
					b.Priority = 1
					b.Credentials = map[string]any{"api_key": "fixture-B", "base_url": upstream.URL}
					repo.accounts = []service.Account{*a, b}
					*ar = repo
				})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			request := []byte(`{"type":"response.create","model":"gpt-5.4","input":"hello"}`)
			require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, request))
			_, body, err := h.clientConn.Read(ctx)
			require.NoError(t, err)
			if exhausted {
				require.Equal(t, "model_not_found", gjson.GetBytes(body, "error.code").String())
				_, _, err = h.clientConn.Read(ctx)
				require.Equal(t, coderws.StatusTryAgainLater, coderws.CloseStatus(err))
			} else {
				require.Equal(t, "response.completed", gjson.GetBytes(body, "type").String())
			}
			issue600Finish(t, h, headers)
			mu.Lock()
			require.Equal(t, []string{"Bearer fixture-A", "Bearer fixture-B"}, hits)
			mu.Unlock()
			repo.mu.Lock()
			require.False(t, repo.canceledWrite)
			require.NotEmpty(t, repo.writes)
			repo.mu.Unlock()
			if !exhausted {
				stickyCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
				stickyCtx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
				stickyCtx.Request.Header = headers.Clone()
				sessionHash := h.handler.gatewayService.GenerateSessionHash(stickyCtx, request)
				require.NotEmpty(t, sessionHash)
				require.NoError(t, h.handler.gatewayService.BindStickySession(ctx, h.apiKey.GroupID, sessionHash, 1))
				next, _, err := coderws.Dial(ctx, h.wsURL, &coderws.DialOptions{HTTPHeader: headers})
				require.NoError(t, err)
				require.NoError(t, next.Write(ctx, coderws.MessageText, request))
				_, body, err = next.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, "response.completed", gjson.GetBytes(body, "type").String())
				_ = next.CloseNow()
				select {
				case <-h.handlerDone:
				case <-ctx.Done():
					t.Fatal("second handler did not exit")
				}
				mu.Lock()
				require.Equal(t, []string{"Bearer fixture-A", "Bearer fixture-B", "Bearer fixture-B"}, hits)
				mu.Unlock()
				// The same sticky A binding must remain usable for another model.
				require.NoError(t, h.handler.gatewayService.BindStickySession(ctx, h.apiKey.GroupID, sessionHash, 1))
				other, _, err := coderws.Dial(ctx, h.wsURL, &coderws.DialOptions{HTTPHeader: headers})
				require.NoError(t, err)
				require.NoError(t, other.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.6","input":"hello"}`)))
				_, body, err = other.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, "response.completed", gjson.GetBytes(body, "type").String())
				_ = other.CloseNow()
				select {
				case <-h.handlerDone:
				case <-ctx.Done():
					t.Fatal("other-model handler did not exit")
				}
				mu.Lock()
				require.Equal(t, []string{"Bearer fixture-A", "Bearer fixture-B", "Bearer fixture-B", "Bearer fixture-A"}, hits)
				mu.Unlock()
			}
		})
	}
}

func TestIssue907HandlerFailedAfterWarmupRequiresReconnect(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			const failure = `{"type":"response.failed","response":{"id":"resp_rejected907","status":"failed","error":{"status":404,"code":"model_not_found","message":"model not found"},"usage":{"input_tokens":2,"output_tokens":0}}}`
			const task = `{"type":"response.create","model":"gpt-5.4","input":[{"type":"reasoning","encrypted_content":"synthetic"},{"type":"function_call","call_id":"fixture","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"fixture","output":"done"}]}`
			var mu sync.Mutex
			var frames [][]byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				for {
					_, body, err := conn.Read(r.Context())
					if err != nil {
						return
					}
					mu.Lock()
					frames = append(frames, append([]byte(nil), body...))
					mu.Unlock()
					reply := failure
					if gjson.GetBytes(body, "generate").Type == gjson.False {
						reply = `{"type":"response.completed","response":{"id":"resp_warmup907","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0}}}`
					}
					if conn.Write(r.Context(), coderws.MessageText, []byte(reply)) != nil {
						return
					}
				}
			}))
			defer upstream.Close()
			repo := &issue907Repo{}
			headers := http.Header{"Session_id": {"fixture907-warmup"}}
			h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, upstream.URL, headers, nil, nil,
				func(key *service.APIKey) { key.AllowLongContext = true },
				func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, ar *service.AccountRepository) {
					cfg.Gateway.MaxAccountSwitches = 2
					a.ID = 1
					a.Credentials = map[string]any{"api_key": "fixture-A", "base_url": upstream.URL}
					a.Extra = map[string]any{"openai_apikey_responses_websockets_v2_mode": mode}
					repo.accounts = []service.Account{*a}
					*ar = repo
				})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","generate":false,"input":[]}`)))
			_, reply, err := h.clientConn.Read(ctx)
			require.NoError(t, err)
			require.Equal(t, "response.completed", gjson.GetBytes(reply, "type").String())
			require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(task)))
			_, reply, err = h.clientConn.Read(ctx)
			require.NoError(t, err)
			require.JSONEq(t, failure, string(reply))
			_, _, err = h.clientConn.Read(ctx)
			require.Equal(t, coderws.StatusTryAgainLater, coderws.CloseStatus(err))
			issue600Finish(t, h, headers)
			mu.Lock()
			defer mu.Unlock()
			require.Len(t, frames, 2, "must never replay warmup or submit the tool task twice")
			require.JSONEq(t, gjson.Get(task, "input").Raw, gjson.GetBytes(frames[1], "input").Raw)
			repo.mu.Lock()
			defer repo.mu.Unlock()
			require.Equal(t, []int64{1}, repo.writes)
		})
	}
}
