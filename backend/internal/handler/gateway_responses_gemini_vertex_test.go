//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Redirect only at the wire boundary: the production builder must first choose
// Vertex, map the model, parse the SA and obtain a token from Gemini's provider.
type responsesVertexWire struct {
	client *http.Client
	target *url.URL
	calls  atomic.Int32
	urls   chan string
}

func (u *responsesVertexWire) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.calls.Add(1)
	u.urls <- req.URL.String()
	local := req.Clone(req.Context())
	local.URL.Scheme, local.URL.Host, local.Host = u.target.Scheme, u.target.Host, u.target.Host
	return u.client.Do(local)
}

func (u *responsesVertexWire) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

type responsesVertexKeyRepo struct {
	service.APIKeyRepository
	key *service.APIKey
}

func (r *responsesVertexKeyRepo) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	if key != r.key.Key {
		return nil, service.ErrAPIKeyNotFound
	}
	copy := *r.key
	return &copy, nil
}

func (*responsesVertexKeyRepo) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

const responsesVertexUsage = `"usageMetadata":{"promptTokenCount":12,"cachedContentTokenCount":3,"candidatesTokenCount":5,"thoughtsTokenCount":2}`

// Each case exercises real API-key authentication, the Responses handler,
// SelectAccountWithLoadAwareness, Gemini forwarding and RecordUsage. The only
// provider endpoint is a local HTTP server and the token cache is synthetic.
func TestGatewayResponsesGeminiVertexHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, name := range []string{"json", "sse", "function_json", "function_sse", "schema_json", "invalid_200_json", "empty_200_json", "early_eof_usage", "read_error_usage", "empty_eof", "idle_usage", "cancel_usage", "cancel_no_usage", "upstream_400", "continuation_rejected", "missing_service", "missing_service_heartbeat", "oauth_rejected", "choice_none_json", "choice_none_sse", "choice_auto_json", "choice_auto_sse", "choice_required_json", "choice_required_sse", "choice_function_json", "choice_function_sse", "choice_undeclared_rejected", "choice_invalid_rejected", "choice_empty_required_rejected"} {
		t.Run(name, func(t *testing.T) {
			stream := !strings.HasSuffix(name, "json")
			tool := strings.HasPrefix(name, "function_") || strings.HasPrefix(name, "choice_required_") || strings.HasPrefix(name, "choice_function_")
			choiceCase := strings.HasPrefix(name, "choice_")
			var wireCanceled atomic.Bool
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				if r.Header.Get("Authorization") != "Bearer ya29.vertex-token" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Anthropic-Version") != "" {
					t.Errorf("wrong Vertex credential headers")
				}
				if !strings.Contains(string(body), "synthetic-926") || gjson.GetBytes(body, "contents.0.role").String() != "user" {
					t.Errorf("wrong Gemini request: %s", body)
				}
				if tool && !strings.Contains(string(body), "functionDeclarations") {
					t.Error("function declaration lost")
				}
				if choiceCase {
					mode := "AUTO"
					if strings.HasPrefix(name, "choice_none_") {
						mode = "NONE"
					}
					if tool {
						mode = "ANY"
					}
					if got := gjson.GetBytes(body, "toolConfig.functionCallingConfig.mode").String(); got != mode {
						t.Errorf("tool_choice lost on Vertex wire: got %q want %q", got, mode)
					}
					allowed := gjson.GetBytes(body, "toolConfig.functionCallingConfig.allowedFunctionNames")
					if strings.HasPrefix(name, "choice_function_") {
						if allowed.Raw != `["lookup"]` {
							t.Errorf("forced function choice lost on Vertex wire: %s", body)
						}
					} else if allowed.Exists() {
						t.Error("unforced choice unexpectedly restricted functions")
					}
				}
				if name == "schema_json" {
					if gjson.GetBytes(body, "generationConfig.responseJsonSchema.properties.id.minimum").Raw != "9007199254740993" {
						t.Errorf("JSON schema integer changed: %s", body)
					}
				}
				if name == "invalid_200_json" {
					_, _ = io.WriteString(w, `{"error":{"message":"invalid payload"}}`)
					return
				}
				if name == "empty_200_json" {
					_, _ = io.WriteString(w, `{}`)
					return
				}
				w.Header().Set("X-Goog-Request-Id", "vertex-fixture-926")
				if name == "upstream_400" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"synthetic invalid argument"}}`)
					return
				}
				part := `{"text":"OK"}`
				if tool {
					part = `{"functionCall":{"name":"lookup","args":{"city":"Seoul"}}}`
				}
				chunk := fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[%s]}}],%s}`, part, responsesVertexUsage)
				if !stream {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, strings.Replace(chunk, `"content":`, `"finishReason":"STOP","content":`, 1))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if name == "empty_eof" {
					return
				}
				if name == "cancel_no_usage" {
					chunk = `{"candidates":[{"content":{"parts":[{"text":"OK"}]}}]}`
				}
				_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
				w.(http.Flusher).Flush()
				switch name {
				case "early_eof_usage":
					return
				case "read_error_usage":
					conn, _, hijackErr := w.(http.Hijacker).Hijack()
					if hijackErr != nil {
						t.Error(hijackErr)
						return
					}
					_ = conn.Close() // terminate HTTP chunking without a final chunk
					return
				case "idle_usage", "cancel_usage", "cancel_no_usage":
					select {
					case <-r.Context().Done():
						wireCanceled.Store(true)
					case <-time.After(4 * time.Second):
						t.Error("Vertex request was not canceled/closed")
					}
					return
				}
				_, _ = fmt.Fprintf(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}],%s}\n\n", responsesVertexUsage)
			}))
			t.Cleanup(provider.Close)
			target, err := url.Parse(provider.URL)
			require.NoError(t, err)
			wire := &responsesVertexWire{client: provider.Client(), target: target, urls: make(chan string, 4)}
			cfg := &config.Config{RunMode: config.RunModeSimple, Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 1}}
			cfg.Default.RateMultiplier = 1
			group := &service.Group{ID: 9260, Platform: service.PlatformGemini, Hydrated: true, Status: service.StatusActive, RateMultiplier: 1}
			account := &service.Account{
				ID: 9261, Platform: service.PlatformGemini, Type: service.AccountTypeServiceAccount,
				Status: service.StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{
					"location": "global", "model_mapping": map[string]any{"gemini-4-carbon": "gemini-2.5-flash"},
					"service_account_json": map[string]any{"type": "service_account", "project_id": "fixture-926", "client_email": "fixture@example.invalid", "private_key": "synthetic-cached-key"},
				},
				AccountGroups: []service.AccountGroup{{AccountID: 9261, GroupID: group.ID}},
			}
			if name == "oauth_rejected" {
				account.Type = service.AccountTypeOAuth
			}
			snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
			usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 4)}
			slots := &concurrencyCacheMock{acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }, acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }}
			concurrency := service.NewConcurrencyService(slots)
			gw := service.NewGatewayService(
				nil, &fakeGroupRepo{group: group}, usage, nil, nil, nil, nil, nil, cfg,
				snapshot, concurrency, service.NewBillingService(cfg, nil), nil, nil, nil, wire,
				&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			)
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			key := &service.APIKey{ID: 9262, Key: "sk-synthetic-926", UserID: 9263, Status: service.StatusActive, GroupID: &group.ID, Group: group,
				User: &service.User{ID: 9263, Status: service.StatusActive, Balance: 100, Concurrency: 2}}
			keyService := service.NewAPIKeyService(&responsesVertexKeyRepo{key: key}, nil, nil, nil, nil, nil, cfg)
			h := &GatewayHandler{gatewayService: gw, apiKeyService: keyService, billingCacheService: billing, cfg: cfg, maxAccountSwitches: 1,
				concurrencyHelper:   NewConcurrencyHelper(concurrency, SSEPingFormatNone, 0),
				geminiCompatService: service.NewGeminiMessagesCompatService(nil, nil, nil, nil, service.NewGeminiTokenProvider(nil, geminiVertexModelsTokenCache{}, nil), nil, wire, nil, cfg)}
			if strings.HasPrefix(name, "missing_service") {
				h.geminiCompatService = nil
			}
			router := gin.New()
			router.Use(gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keyService, nil, cfg)))
			router.POST("/v1/responses", func(c *gin.Context) {
				if name == "missing_service_heartbeat" {
					c.Header("Content-Type", "text/event-stream")
					_, _ = io.WriteString(c.Writer, ": ping\n\n")
					c.Writer.Flush()
				}
				h.Responses(c)
			})
			input := map[string]any{"model": "gemini-4-carbon", "input": "synthetic-926", "stream": stream}
			if name == "schema_json" {
				input["text"] = json.RawMessage(`{"format":{"type":"json_schema","name":"fixture","schema":{"type":"object","properties":{"id":{"type":"integer","minimum":9007199254740993}}}}}`)
			}
			if tool || choiceCase && name != "choice_empty_required_rejected" {
				input["tools"] = []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}}}
			}
			if choiceCase {
				switch {
				case strings.HasPrefix(name, "choice_none_"):
					input["tool_choice"] = "none"
				case strings.HasPrefix(name, "choice_auto_"):
					input["tool_choice"] = "auto"
				case strings.HasPrefix(name, "choice_required_"), name == "choice_empty_required_rejected":
					input["tool_choice"] = "required"
				case strings.HasPrefix(name, "choice_function_"):
					input["tool_choice"] = map[string]any{"type": "function", "name": "lookup"}
				case name == "choice_undeclared_rejected":
					input["tool_choice"] = map[string]any{"type": "function", "name": "missing"}
				default:
					input["tool_choice"] = "unsupported"
				}
			}
			if name == "continuation_rejected" {
				input["previous_response_id"] = "resp_synthetic"
			}
			body, err := json.Marshal(input)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body))).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+key.Key)
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			var responseWriter http.ResponseWriter = recorder
			if strings.HasPrefix(name, "cancel_") {
				responseWriter = &responsesVertexCancelWriter{ResponseWriter: recorder, cancel: cancel}
			}
			router.ServeHTTP(responseWriter, req)
			out := recorder.Body.String()
			choiceReject := choiceCase && strings.HasSuffix(name, "_rejected")
			localReject := choiceReject || name == "continuation_rejected" || strings.HasPrefix(name, "missing_service") || name == "oauth_rejected"
			require.Equal(t, int32(1), atomic.LoadInt32(&slots.releaseUserCalled))
			require.Equal(t, int32(1), atomic.LoadInt32(&slots.releaseAccountCalled))
			if localReject {
				require.Zero(t, wire.calls.Load())
				require.Empty(t, usage.created)
				if choiceReject {
					require.Equal(t, 400, recorder.Code)
					require.Contains(t, out, "tool_choice")
				} else if name == "continuation_rejected" {
					require.Equal(t, 400, recorder.Code)
					require.Contains(t, out, "previous_response_id")
				} else if name == "oauth_rejected" {
					require.Equal(t, 400, recorder.Code)
					require.Contains(t, out, "Gemini OAuth accounts do not support Responses")
				} else if name == "missing_service_heartbeat" {
					require.Equal(t, 200, recorder.Code)
					require.Contains(t, out, ": ping\n\n")
					require.Equal(t, 1, strings.Count(out, "event: response.failed\n"))
					require.NotContains(t, out, "event: response.completed")
				} else {
					require.Equal(t, 502, recorder.Code)
					require.Contains(t, out, "Gemini compatibility service")
				}
				return
			}
			require.Equal(t, int32(1), wire.calls.Load(), "gemini_vertex_926_wire: selected Gemini SA must reach Vertex once")
			upstreamURL := <-wire.urls
			require.Contains(t, upstreamURL, "https://aiplatform.googleapis.com/v1/projects/fixture-926/locations/global/publishers/google/models/gemini-2.5-flash:")
			if stream {
				require.Contains(t, upstreamURL, "streamGenerateContent?alt=sse")
			} else {
				require.True(t, strings.HasSuffix(upstreamURL, ":generateContent"))
			}
			if name == "invalid_200_json" || name == "empty_200_json" {
				require.Equal(t, 502, recorder.Code)
				require.NotContains(t, out, `"status":"completed"`)
				require.Empty(t, usage.created)
				return
			}
			if name == "upstream_400" {
				require.Equal(t, 400, recorder.Code)
				require.Contains(t, out, "synthetic invalid argument")
				require.Empty(t, usage.created)
				return
			}
			require.Equal(t, 200, recorder.Code, out)
			failed := name == "early_eof_usage" || name == "read_error_usage" || name == "empty_eof" || name == "idle_usage"
			canceled := strings.HasPrefix(name, "cancel_")
			if stream {
				require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
				require.NotContains(t, out, "[DONE]")
				if failed {
					require.Equal(t, 1, strings.Count(out, "event: response.failed\n"))
					require.NotContains(t, out, "event: response.completed")
				} else if canceled {
					require.NotContains(t, out, "event: response.completed")
					require.NotContains(t, out, "event: response.failed")
				} else {
					require.Equal(t, 1, strings.Count(out, "event: response.completed\n"))
				}
				if tool {
					require.Contains(t, out, "response.function_call_arguments.delta")
					require.Contains(t, out, "lookup")
					require.Contains(t, out, "Seoul")
				}
				sequence := int64(-1)
				for _, line := range strings.Split(out, "\n") {
					if strings.HasPrefix(line, "data: ") {
						data := strings.TrimPrefix(line, "data: ")
						require.True(t, gjson.Valid(data))
						next := gjson.Get(data, "sequence_number").Int()
						require.Equal(t, sequence+1, next)
						sequence = next
						if gjson.Get(data, "type").String() == "response.completed" {
							require.Equal(t, int64(12), gjson.Get(data, "response.usage.input_tokens").Int())
							require.Equal(t, int64(7), gjson.Get(data, "response.usage.output_tokens").Int())
							require.Equal(t, int64(3), gjson.Get(data, "response.usage.input_tokens_details.cached_tokens").Int())
						}
					}
				}
			} else {
				require.Equal(t, "response", gjson.Get(out, "object").String())
				require.Equal(t, "completed", gjson.Get(out, "status").String())
				require.Equal(t, int64(12), gjson.Get(out, "usage.input_tokens").Int())
				require.Equal(t, int64(7), gjson.Get(out, "usage.output_tokens").Int())
				if tool {
					require.Equal(t, "function_call", gjson.Get(out, "output.0.type").String())
					require.Equal(t, "lookup", gjson.Get(out, "output.0.name").String())
				} else {
					require.Contains(t, out, "OK")
				}
			}
			if name == "empty_eof" || name == "cancel_no_usage" {
				require.Empty(t, usage.created)
			} else {
				require.Len(t, usage.created, 1, "observed usage must survive failure/cancellation and be recorded exactly once")
				log := <-usage.created
				require.Equal(t, 9, log.InputTokens)
				require.Equal(t, 7, log.OutputTokens)
				require.Equal(t, 3, log.CacheReadTokens)
				require.Equal(t, account.ID, log.AccountID)
			}
			if canceled || name == "idle_usage" {
				require.Eventually(t, wireCanceled.Load, time.Second, 10*time.Millisecond, "actual Vertex HTTP request must close")
			}
		})
	}
}

type responsesVertexCancelWriter struct {
	http.ResponseWriter
	cancel context.CancelFunc
}

func (w *responsesVertexCancelWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseWriter.Write(body)
	if strings.Contains(string(body), "response.output_text.delta") {
		w.cancel()
	}
	return n, err
}

func (w *responsesVertexCancelWriter) Flush() { w.ResponseWriter.(http.Flusher).Flush() }
