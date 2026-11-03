//go:build unit

package handler

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Capture the request at the actual transport boundary, after account mapping,
// protocol conversion and Codex normalization have all completed.
type apiKeyPolicyForwardingUpstream struct {
	mu     sync.Mutex
	bodies [][]byte
	paths  []string
}

func (u *apiKeyPolicyForwardingUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.bodies = append(u.bodies, append([]byte(nil), body...))
	u.paths = append(u.paths, req.URL.Path)
	u.mu.Unlock()
	model := gjson.GetBytes(body, "model").String()
	contentType := "text/event-stream"
	completion := fmt.Sprintf(`{"id":"resp_policy","object":"response","status":"completed","model":%q,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`, model)
	wire := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"done\",\"output_index\":0,\"content_index\":0}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + completion + "}\n\n"
	if strings.HasSuffix(req.URL.Path, "/compact") {
		contentType, wire = "application/json", completion
	} else if strings.HasSuffix(req.URL.Path, "/chat/completions") {
		contentType = "application/json"
		wire = fmt.Sprintf(`{"id":"chatcmpl_policy","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`, model)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
}

func (u *apiKeyPolicyForwardingUpstream) DoWithTLS(req *http.Request, proxy string, account int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, account, concurrency)
}

func TestAPIKeyPolicyHTTPFinalUpstreamPayloadAndKeyIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"oauth", "apikey", "passthrough", "raw-chat"} {
		for _, endpoint := range []string{"responses", "responses/compact", "chat/completions", "messages"} {
			if mode == "raw-chat" && endpoint == "responses/compact" {
				continue
			}
			t.Run(mode+"/"+endpoint, func(t *testing.T) {
				upstream := &apiKeyPolicyForwardingUpstream{}
				repo := &initialAdmissionRepo{}
				h := newInitialAdmissionHandler(t, repo, upstream)
				account := service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
					Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3131}, Concurrency: 1,
					Credentials: map[string]any{"api_key": "test-upstream-key"},
					Extra:       map[string]any{"openai_responses_supported": true},
				}
				if mode == "oauth" {
					account.Type = service.AccountTypeOAuth
					account.Credentials = map[string]any{"access_token": "test-access-token", "chatgpt_account_id": "test-account"}
				}
				if mode == "passthrough" {
					account.Extra["openai_passthrough"] = true
				}
				if mode == "raw-chat" {
					account.Extra["openai_responses_supported"] = false
					account.Extra["openai_responses_mode"] = "disabled"
				}
				repo.accounts = []service.Account{account}
				group := &service.Group{ID: 3131, Platform: service.PlatformOpenAI, Status: service.StatusActive, AllowMessagesDispatch: true}
				keys := map[string]*service.APIKey{
					"key-a": {ID: 11, GroupID: &group.ID, Group: group, User: &service.User{ID: 100}, ModelMapping: map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}, ReasoningEffort: "high"},
					"key-b": {ID: 12, GroupID: &group.ID, Group: group, User: &service.User{ID: 100}},
				}
				router := gin.New()
				router.Use(func(c *gin.Context) {
					c.Set(string(middleware2.ContextKeyAPIKey), keys[c.GetHeader("X-Api-Key")])
					c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100})
					c.Next()
				})
				router.Use(middleware2.GroupModelAllowlist(), middleware2.APIKeyModelPolicy())
				path := "/v1/" + endpoint
				forward := h.Responses
				body := `{"model":"gpt-6.1-sol","input":"hello","stream":false,"reasoning":{"effort":"low"}}`
				if endpoint == "chat/completions" {
					forward = h.ChatCompletions
					body = `{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"hello"}],"stream":false,"reasoning_effort":"low"}`
				} else if endpoint == "messages" {
					forward = h.Messages
					body = `{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"hello"}],"max_tokens":32,"stream":false,"output_config":{"effort":"low"}}`
				}
				router.POST(path, forward)
				for i, key := range []string{"key-a", "key-b", "key-a"} {
					requestBody := body
					if i == 2 {
						for _, field := range []string{"reasoning", "reasoning_effort", "output_config"} {
							var err error
							requestBody, err = sjson.Delete(requestBody, field)
							require.NoError(t, err)
						}
					}
					req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(requestBody))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("X-Api-Key", key)
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, req)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					upstream.mu.Lock()
					bodies := append([][]byte(nil), upstream.bodies...)
					paths := append([]string(nil), upstream.paths...)
					upstream.mu.Unlock()
					require.Len(t, bodies, i+1, "one successful upstream request per client request")
					wantModel, wantEffort := "gpt-5.6-sol", "high"
					if key == "key-b" {
						wantModel, wantEffort = "gpt-6.1-sol", "low"
					}
					require.Equal(t, wantModel, gjson.GetBytes(bodies[i], "model").String())
					effortPath := "reasoning.effort"
					if strings.HasSuffix(paths[i], "/chat/completions") {
						effortPath = "reasoning_effort"
					}
					require.Equal(t, wantEffort, gjson.GetBytes(bodies[i], effortPath).String(), string(bodies[i]))
				}
			})
		}
	}
}

func TestAPIKeyPolicyWSHandlerFinalPayloadAndKeyIsolation(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		for _, configured := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/configured=%t", mode, configured), func(t *testing.T) {
				key := wsRevalidationKey(0, true, []string{"gpt-6.1-sol", "gpt-5.6-sol"}, false)
				if configured {
					key.ModelMapping = map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}
					key.ReasoningEffort = "high"
				}
				h := newWSRevalidationHarness(t, key, service.APIKeyQueuePolicy{}, mode)
				for i, payload := range []string{
					`{"type":"response.create","model":"gpt-6.1-sol","reasoning":{"effort":"low"},"stream":false}`,
					`{"type":"response.create","reasoning":{"effort":"low"},"stream":false}`,
				} {
					if configured && i == 1 {
						payload = `{"type":"response.create","stream":false}`
					}
					h.sendTurn(payload)
					h.requireCompleted(5 * time.Second)
				}
				h.upstream.mu.Lock()
				frames := append([][]byte(nil), h.upstream.frames...)
				h.upstream.mu.Unlock()
				require.Len(t, frames, 2)
				for _, frame := range frames {
					wantModel, wantEffort := "gpt-6.1-sol", "low"
					if configured {
						wantModel, wantEffort = "gpt-5.6-sol", "high"
					}
					require.Equal(t, wantModel, gjson.GetBytes(frame, "model").String())
					require.Equal(t, wantEffort, gjson.GetBytes(frame, "reasoning.effort").String())
				}
			})
		}
	}
}

func TestAPIKeyPolicyWSChangesRequireReconnectBeforeNextUpstreamFrame(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		for _, change := range []string{"model", "effort"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				key := wsRevalidationKey(0, false, nil, false)
				key.ModelMapping = map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}
				key.ReasoningEffort = "high"
				h := newWSRevalidationHarness(t, key, service.APIKeyQueuePolicy{}, mode)
				h.sendTurn(`{"type":"response.create","model":"gpt-6.1-sol","stream":false}`)
				h.requireCompleted(5 * time.Second)
				latest := *key
				if change == "model" {
					latest.ModelMapping = map[string]string{"gpt-6.1-sol": "gpt-5.6-luna"}
				} else {
					latest.ReasoningEffort = "low"
				}
				h.repo.set(&latest)
				h.sendTurn(`{"type":"response.create","stream":false}`)
				h.requireClosed(coderws.StatusTryAgainLater, "configuration changed")
				require.Equal(t, 1, h.upstream.frameCount(), "the old policy must never forward a second request")
			})
		}
	}
}
