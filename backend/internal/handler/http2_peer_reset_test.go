//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	coderws "github.com/coder/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Use the real Go HTTP/2 reader, including its unexported StreamError type.
// Only routing/TLS trust is injected; the service, handler, capture middleware,
// persistence preparation and user DTO mapper are production implementations.
type peerResetHTTPUpstream struct {
	service.HTTPUpstream
	client *http.Client
	base   *url.URL
}

func (u *peerResetHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	req.URL.Scheme, req.URL.Host = u.base.Scheme, u.base.Host
	return u.client.Do(req)
}

func TestHTTP2PeerResetBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupOpsErrorLogTestQueue(t, 8)
	var submissions atomic.Int32
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Error("fixture must negotiate real HTTP/2")
		}
		_, _ = io.Copy(io.Discard, r.Body)
		submissions.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
		w.(http.Flusher).Flush()
		// net/http sends RST_STREAM(INTERNAL_ERROR), without a successful terminal.
		panic(http.ErrAbortHandler)
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()
	base, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	h := newHTTP2PeerResetHandler(t, &peerResetHTTPUpstream{client: upstream.Client(), base: base})
	repo := &ingressRejectOpsRepo{}
	ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	var outcomes []service.AccountConcurrencyResult
	ops.SetAutoConfigObserver(func(result service.AccountConcurrencyResult) { outcomes = append(outcomes, result) })
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		seed, _ := newOpenAIResponsesFailoverTestContext(t, c.Request.Context())
		for key, value := range seed.Keys {
			c.Set(key, value)
		}
		h.Responses(c)
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1","stream":true,"input":"synthetic"}`)))
	for len(opsErrorLogQueue) > 0 {
		flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
	}
	require.Equal(t, int32(1), submissions.Load(), "submitted request must never be replayed")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "partial")
	require.Len(t, repo.entries, 1)
	entry := repo.entries[0]
	require.Equal(t, "transport_error", entry.ErrorType, "peer reset must persist as transport_error, not synthetic HTTP 502")
	require.Equal(t, http.StatusOK, entry.StatusCode, "persist committed wire status")
	require.Nil(t, entry.UpstreamStatusCode, "a reset has no upstream HTTP error status")
	require.False(t, entry.IsBusinessLimited)
	require.Len(t, outcomes, 1)
	require.False(t, outcomes[0].Success)
	body := rec.Body.String()
	require.Equal(t, 1, strings.Count(body, "event: response.failed\n"))
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			event := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if event.Get("type").String() == "response.failed" {
				var strict responsesFailedEvent
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &strict))
				require.NotEmpty(t, strict.Response.ID)
				require.Equal(t, "response", strict.Response.Object)
				require.Positive(t, strict.Response.CreatedAt)
				require.NotNil(t, strict.Response.Output)
				require.Equal(t, service.OpenAIUpstreamHTTP2StreamErrorCode, strict.Response.Error.Code)
				require.True(t, event.Get("sequence_number").Exists())
				require.Positive(t, event.Get("response.created_at").Int())
				require.Equal(t, "response", event.Get("response.object").String())
				require.True(t, event.Get("response.output").IsArray())
				require.Equal(t, "transport_error", event.Get("response.error.category").String())
			}
		}
	}
	require.NotContains(t, body, "event: error\n")
	require.NotContains(t, body, "response.completed")
	require.NotContains(t, body, "[DONE]")
	for _, safe := range []string{"upstream_http2_stream_error", "reset by peer", "INTERNAL_ERROR", "incomplete", "Do not automatically retry"} {
		require.Contains(t, body, safe)
	}
	detail := service.ToUserErrorRequestDetail(&service.OpsErrorLogDetail{
		OpsErrorLog: service.OpsErrorLog{Type: entry.ErrorType, Phase: entry.ErrorPhase, StatusCode: entry.StatusCode, Message: entry.ErrorMessage},
		ErrorBody:   entry.ErrorBody, UpstreamStatusCode: entry.UpstreamStatusCode,
	})
	payload, err := json.Marshal(detail)
	require.NoError(t, err)
	require.Equal(t, "transport_error", detail.Category)
	for _, unsafe := range []string{"stream ID", "127.0.0.1", "synthetic-", "received from peer"} {
		require.NotContains(t, body, unsafe)
		require.NotContains(t, string(payload), unsafe)
	}
}

func TestHTTP2PeerResetAdaptersNeverReplay(t *testing.T) {
	for _, mode := range []string{"responses", "passthrough", "responses_via_chat", "chat_via_responses", "raw_chat"} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partial=%v", mode, partial), func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 8)
				var submissions atomic.Int32
				upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.ProtoMajor != 2 {
						t.Error("expected HTTP/2")
					}
					_, _ = io.Copy(io.Discard, r.Body)
					submissions.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(200)
					if partial {
						if strings.HasSuffix(r.URL.Path, "/chat/completions") {
							_, _ = io.WriteString(w, "data: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5.1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
						} else {
							_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
						}
					}
					w.(http.Flusher).Flush()
					panic(http.ErrAbortHandler)
				}))
				upstream.EnableHTTP2 = true
				upstream.StartTLS()
				defer upstream.Close()
				base, err := url.Parse(upstream.URL)
				require.NoError(t, err)
				extra := map[string]any{}
				kind := service.AccountTypeOAuth
				if mode == "passthrough" {
					extra["openai_passthrough"] = true
				}
				if mode == "responses_via_chat" || mode == "raw_chat" {
					kind = service.AccountTypeAPIKey
					extra[openai_compat.ExtraKeyResponsesMode] = string(openai_compat.ResponsesSupportModeForceChatCompletions)
				}
				accounts := []service.Account{}
				for i := int64(1); i <= 2; i++ {
					accounts = append(accounts, service.Account{ID: i, Name: "synthetic", Platform: service.PlatformOpenAI, Type: kind, Status: service.StatusActive, Schedulable: true, Priority: int(i), Credentials: map[string]any{"api_key": "synthetic", "access_token": "synthetic", "base_url": "https://example.invalid"}, Extra: extra})
				}
				h := newHTTP2PeerResetHandler(t, &peerResetHTTPUpstream{client: upstream.Client(), base: base}, accounts...)
				repo := &ingressRejectOpsRepo{}
				ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				var outcomes []service.AccountConcurrencyResult
				ops.SetAutoConfigObserver(func(r service.AccountConcurrencyResult) { outcomes = append(outcomes, r) })
				endpoint := "/v1/responses"
				body := `{"model":"gpt-5.1","stream":true,"input":"synthetic"}`
				handler := h.Responses
				if mode == "chat_via_responses" || mode == "raw_chat" {
					endpoint = "/v1/chat/completions"
					body = `{"model":"gpt-5.1","stream":true,"messages":[{"role":"user","content":"synthetic"}]}`
					handler = h.ChatCompletions
				}
				router := gin.New()
				router.Use(OpsErrorLoggerMiddleware(ops))
				router.POST(endpoint, func(c *gin.Context) {
					seed, _ := newOpenAIResponsesFailoverTestContext(t, c.Request.Context())
					for k, v := range seed.Keys {
						c.Set(k, v)
					}
					handler(c)
				})
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body)))
				for len(opsErrorLogQueue) > 0 {
					flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
				}
				require.Equal(t, int32(1), submissions.Load(), "a submitted request must never be replayed after peer reset")
				require.Len(t, repo.entries, 1, rec.Body.String())
				entry := repo.entries[0]
				require.Equal(t, "transport_error", entry.ErrorType)
				require.Nil(t, entry.UpstreamStatusCode)
				require.Equal(t, rec.Code, entry.StatusCode)
				require.Len(t, outcomes, 1)
				require.False(t, outcomes[0].Success)
				outcomeJSON, err := json.Marshal(outcomes[0])
				require.NoError(t, err)
				require.Contains(t, string(outcomeJSON), `"FailureCategory":"transport_error"`)
				if partial {
					require.Equal(t, 200, rec.Code)
					require.Contains(t, rec.Body.String(), "partial")
					terminal := "event: response.failed\n"
					if endpoint == "/v1/chat/completions" {
						terminal = "event: error\n"
					}
					require.Equal(t, 1, strings.Count(rec.Body.String(), terminal))
				} else {
					require.Equal(t, 502, rec.Code)
					require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
					require.True(t, json.Valid(rec.Body.Bytes()))
				}
				require.Contains(t, rec.Body.String(), "upstream_http2_stream_error")
				require.Contains(t, rec.Body.String(), "INTERNAL_ERROR")
				for _, forbidden := range []string{"[DONE]", "response.completed", "stream ID", "127.0.0.1"} {
					require.NotContains(t, rec.Body.String(), forbidden)
				}
			})
		}
	}
}

func TestHTTP2PeerResetBeforeHeaders(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 8)
	var submissions atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.ProtoMajor != 2 {
			t.Error("expected HTTP/2")
		}
		submissions.Add(1)
		panic(http.ErrAbortHandler) // consumed POST; no response headers
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	base, err := url.Parse(server.URL)
	require.NoError(t, err)
	h := newHTTP2PeerResetHandler(t, &peerResetHTTPUpstream{client: server.Client(), base: base})
	slots := &helperConcurrencyCacheStub{userSeq: []bool{true}}
	h.concurrencyHelper = NewConcurrencyHelper(service.NewConcurrencyService(slots), SSEPingFormatComment, 0)
	repo := &ingressRejectOpsRepo{}
	ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	var outcomes []service.AccountConcurrencyResult
	ops.SetAutoConfigObserver(func(r service.AccountConcurrencyResult) { outcomes = append(outcomes, r) })
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		seed, _ := newOpenAIResponsesFailoverTestContext(t, c.Request.Context())
		for k, v := range seed.Keys {
			c.Set(k, v)
		}
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 1})
		h.Responses(c)
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1","stream":true,"input":"synthetic"}`)))
	for len(opsErrorLogQueue) > 0 {
		flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
	}
	require.Equal(t, int32(1), submissions.Load(), "peer reset before headers must not replay a consumed POST")
	require.Equal(t, 502, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.True(t, json.Valid(rec.Body.Bytes()))
	require.Contains(t, rec.Body.String(), "upstream_http2_stream_error")
	require.NotContains(t, rec.Body.String(), "stream ID")
	require.Len(t, repo.entries, 1)
	require.Equal(t, "transport_error", repo.entries[0].ErrorType)
	require.Nil(t, repo.entries[0].UpstreamStatusCode)
	require.Len(t, outcomes, 1)
	require.False(t, outcomes[0].Success)
	data, err := json.Marshal(outcomes[0])
	require.NoError(t, err)
	require.Contains(t, string(data), `"FailureCategory":"transport_error"`)
	require.Equal(t, 1, slots.userAcquireCalls)
	require.Equal(t, 1, slots.userReleaseCalls, "reset must release acquired user capacity")
}

func TestHTTP2PeerResetPartialImages(t *testing.T) {
	for _, kind := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
		t.Run(kind, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 8)
			var submissions atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if gjson.GetBytes(body, "n").Int() != 2 {
					t.Error("expected multi-image request")
				}
				submissions.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"aW1hZ2U=\",\"output_format\":\"png\",\"size\":\"1024x1024\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}\n\n")
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler)
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			defer server.Close()
			base, err := url.Parse(server.URL)
			require.NoError(t, err)
			cfg := &config.Config{RunMode: config.RunModeSimple}
			accounts := openAIImagesFailoverAccountRepo{accounts: []service.Account{{ID: 1, Name: "synthetic", Platform: service.PlatformOpenAI, Type: kind, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3131}, Credentials: map[string]any{"api_key": "synthetic", "access_token": "synthetic", "base_url": "https://example.invalid"}}}}
			usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 2)}
			deferred := service.NewDeferredService(accounts, nil, time.Minute)
			gateway := service.NewOpenAIGatewayService(accounts, nil, usage, nil, nil, nil, nil, nil, cfg, nil, service.NewConcurrencyService(&helperConcurrencyCacheStub{}), service.NewBillingService(cfg, nil), nil, nil, &peerResetHTTPUpstream{client: server.Client(), base: base}, deferred, nil, nil, nil, nil, nil, nil, nil)
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
			repo := &ingressRejectOpsRepo{}
			ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			var schedulerOutcomes []bool
			t.Cleanup(service.ObserveHTTP2SchedulerForTest(gateway, func(id int64, success bool) {
				require.EqualValues(t, 1, id)
				schedulerOutcomes = append(schedulerOutcomes, success)
			}))
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/images/generations", func(c *gin.Context) {
				seed, _ := newOpenAIResponsesFailoverTestContext(t, c.Request.Context())
				for k, v := range seed.Keys {
					c.Set(k, v)
				}
				key, _ := middleware2.GetAPIKeyFromContext(c)
				key.Group.AllowImageGeneration = true
				h.Images(c)
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"synthetic","n":2,"stream":true}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			for len(opsErrorLogQueue) > 0 {
				flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
			}
			require.Equal(t, int32(1), submissions.Load())
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), "aW1hZ2U=")
			require.Len(t, repo.entries, 1, "partial-image reset must persist a failed request")
			require.Equal(t, "transport_error", repo.entries[0].ErrorType)
			require.Equal(t, 200, repo.entries[0].StatusCode)
			require.Nil(t, repo.entries[0].UpstreamStatusCode)
			require.Equal(t, []bool{false}, schedulerOutcomes, "partial image reset must report scheduler failure")
			require.Greater(t, service.HTTP2SchedulerErrorRateForTest(gateway, 1), 0.0, "real scheduler must record failure")
			require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error\n"))
			require.Contains(t, rec.Body.String(), "upstream_http2_stream_error")
			require.NotContains(t, rec.Body.String(), "[DONE]")
			select {
			case log := <-usage.created:
				require.Equal(t, 1, log.ImageCount, "keep billable delivered image without charging for missing image")
			default:
				t.Fatal("partial image usage must be recorded")
			}
		})
	}
}

// Keep billing and deferred usage dependencies real even on failed streams;
// a recovered nil-fixture panic is not valid acceptance evidence.
func newHTTP2PeerResetHandler(t *testing.T, upstream service.HTTPUpstream, accounts ...service.Account) *OpenAIGatewayHandler {
	t.Helper()
	if len(accounts) == 0 {
		for i := int64(1); i <= 2; i++ {
			accounts = append(accounts, service.Account{ID: i, Name: "synthetic", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: int(i), Credentials: map[string]any{"access_token": "synthetic"}})
		}
	}
	for i := range accounts {
		accounts[i].GroupIDs = []int64{3131, 3132}
	}
	repo := openAIImagesFailoverAccountRepo{accounts: accounts}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	gateway := service.NewOpenAIGatewayService(repo, nil, &openAIWSUsageHandlerUsageLogRepoStub{}, nil, nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, billing, upstream, service.NewDeferredService(repo, nil, time.Minute), nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(gateway.CloseOpenAIWSPool)
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	h.maxAccountSwitches = 2
	return h
}

func TestHTTP2PeerResetHTTP502Control(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 8)
	var submissions atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		submissions.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(502)
		_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"synthetic HTTP refusal"}}`)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	base, err := url.Parse(server.URL)
	require.NoError(t, err)
	h := newHTTP2PeerResetHandler(t, &peerResetHTTPUpstream{client: server.Client(), base: base})
	repo := &ingressRejectOpsRepo{}
	ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		seed, _ := newOpenAIResponsesFailoverTestContext(t, c.Request.Context())
		for k, v := range seed.Keys {
			c.Set(k, v)
		}
		h.Responses(c)
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1","stream":true,"input":"synthetic"}`)))
	for len(opsErrorLogQueue) > 0 {
		flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
	}
	require.Equal(t, int32(2), submissions.Load(), "actual HTTP refusal retains existing account failover")
	require.Equal(t, 502, rec.Code)
	require.Len(t, repo.entries, 1)
	require.NotNil(t, repo.entries[0].UpstreamStatusCode)
	require.Equal(t, 502, *repo.entries[0].UpstreamStatusCode)
	require.NotEqual(t, "transport_error", repo.entries[0].ErrorType)
	require.NotContains(t, rec.Body.String(), "upstream_http2_stream_error")
}

func TestHTTP2PeerResetImagesBeforeHeaders(t *testing.T) {
	for _, kind := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
		t.Run(kind, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 8)
			var submissions atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				submissions.Add(1)
				panic(http.ErrAbortHandler)
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			defer server.Close()
			base, err := url.Parse(server.URL)
			require.NoError(t, err)
			account := service.Account{ID: 1, Name: "synthetic", Platform: service.PlatformOpenAI, Type: kind, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "synthetic", "access_token": "synthetic", "base_url": "https://example.invalid"}}
			h := newHTTP2PeerResetHandler(t, &peerResetHTTPUpstream{client: server.Client(), base: base}, account)
			repo := &ingressRejectOpsRepo{}
			ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/images/generations", func(c *gin.Context) {
				seed, _ := newOpenAIResponsesFailoverTestContext(t, c.Request.Context())
				for k, v := range seed.Keys {
					c.Set(k, v)
				}
				key, _ := middleware2.GetAPIKeyFromContext(c)
				key.Group.AllowImageGeneration = true
				h.Images(c)
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"synthetic","stream":true}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			for len(opsErrorLogQueue) > 0 {
				flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
			}
			require.Equal(t, int32(1), submissions.Load())
			require.Len(t, repo.entries, 1)
			require.Equal(t, "transport_error", repo.entries[0].ErrorType, "image reset before headers must persist transport_error")
			require.Nil(t, repo.entries[0].UpstreamStatusCode)
			require.Equal(t, 502, rec.Code)
			require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
			require.Contains(t, rec.Body.String(), "Do not automatically retry")
			require.NotContains(t, rec.Body.String(), "stream ID")
		})
	}
}

func TestHTTP2PeerResetNativeConversionBoundary(t *testing.T) {
	for _, endpoint := range []string{"/v1/responses", "/v1/chat/completions"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", endpoint, stream), func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 8)
				var submissions atomic.Int32
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					submissions.Add(1)
					if !strings.HasSuffix(r.URL.Path, "/messages") {
						t.Error("native Anthropic path was not exercised")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n")
					w.(http.Flusher).Flush()
					panic(http.ErrAbortHandler)
				}))
				server.EnableHTTP2 = true
				server.StartTLS()
				defer server.Close()
				base, err := url.Parse(server.URL)
				require.NoError(t, err)
				account := service.Account{ID: 1, Name: "synthetic", Platform: service.PlatformKimi, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://example.invalid", "api_protocol": service.APIProtocolAnthropic}}
				h := newHTTP2PeerResetHandler(t, &peerResetHTTPUpstream{client: server.Client(), base: base}, account)
				repo := &ingressRejectOpsRepo{}
				ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				router := gin.New()
				router.Use(OpsErrorLoggerMiddleware(ops))
				router.POST(endpoint, func(c *gin.Context) {
					seed, _ := newOpenAIResponsesFailoverTestContext(t, c.Request.Context())
					for k, v := range seed.Keys {
						c.Set(k, v)
					}
					key, _ := middleware2.GetAPIKeyFromContext(c)
					key.Group.Platform = service.PlatformKimi
					if endpoint == "/v1/responses" {
						h.Responses(c)
					} else {
						h.ChatCompletions(c)
					}
				})
				body := fmt.Sprintf(`{"model":"claude-sonnet-4-5","stream":%v,"input":"synthetic","messages":[{"role":"user","content":"synthetic"}]}`, stream)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body)))
				for len(opsErrorLogQueue) > 0 {
					flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
				}
				require.Equal(t, int32(1), submissions.Load())
				require.NotContains(t, rec.Body.String(), "[DONE]", "native reset must not fabricate successful completion")
				require.NotContains(t, rec.Body.String(), "response.completed", "native reset must not fabricate successful completion")
				require.Len(t, repo.entries, 1, "native reset must persist a failed request")
				require.Equal(t, "transport_error", repo.entries[0].ErrorType)
				require.Nil(t, repo.entries[0].UpstreamStatusCode)
				if stream {
					require.Equal(t, 200, rec.Code)
					require.Contains(t, rec.Body.String(), "partial")
				} else {
					require.Equal(t, 502, rec.Code)
				}
				require.Contains(t, rec.Body.String(), "upstream_http2_stream_error")
			})
		}
	}
}

// The fixture drives two real WebSocket turns through the production HTTP
// bridge and actual Ops middleware. Only outer turn orchestration is controlled.
func TestHTTP2PeerResetWebSocketTurnPersistence(t *testing.T) {
	for _, beforeHeaders := range []bool{false, true} {
		t.Run(fmt.Sprint(beforeHeaders), func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 8)
			var submissions atomic.Int32
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				turn := submissions.Add(1)
				if r.ProtoMajor != 2 {
					t.Error("expected HTTP/2 bridge transport")
				}
				if turn == 2 && beforeHeaders {
					panic(http.ErrAbortHandler)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
				if turn == 1 {
					_, _ = io.WriteString(w, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_semantic\",\"status\":\"failed\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"synthetic context limit\"}}}\n\n")
					return
				}
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler)
			}))
			upstream.EnableHTTP2 = true
			upstream.StartTLS()
			defer upstream.Close()
			base, err := url.Parse(upstream.URL)
			require.NoError(t, err)
			account := service.Account{ID: 1, Name: "synthetic", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://example.invalid"}}
			h := newHTTP2PeerResetHandler(t, &peerResetHTTPUpstream{client: upstream.Client(), base: base}, account)
			repo := &ingressRejectOpsRepo{}
			ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			done := make(chan struct{})
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Next(); close(done) })
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.GET("/v1/responses", func(c *gin.Context) {
				conn, acceptErr := coderws.Accept(c.Writer, c.Request, nil)
				if acceptErr != nil {
					t.Error(acceptErr)
					return
				}
				defer func() { _ = conn.CloseNow() }()
				service.SetOpenAIClientTransport(c, service.OpenAIClientTransportWS)
				c.Set(opsAccountIDKey, account.ID)
				c.Set(opsModelKey, "gpt-5.1")
				c.Set(opsStreamKey, true)
				for turn := 1; turn <= 2; turn++ {
					_, payload, readErr := conn.Read(c.Request.Context())
					if readErr != nil {
						t.Error(readErr)
						return
					}
					service.BeginOpsStreamTurn(c, turn)
					turnErr := service.ForwardHTTP2BridgeTurnForTest(h.gatewayService, c, &account, payload, turn, func(frame []byte) error { return conn.Write(c.Request.Context(), coderws.MessageText, frame) })
					if turn == 2 {
						code, _, ok := service.OpenAIUpstreamStreamReadErrorDetails(turnErr)
						if !ok || code != service.OpenAIUpstreamHTTP2StreamErrorCode {
							t.Errorf("reset turn must return classified non-replay error: %v", turnErr)
						}
					}
				}
				_ = conn.Close(coderws.StatusNormalClosure, "fixture complete")
			})
			server := httptest.NewServer(router)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client, upgrade, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			require.NoError(t, err)
			defer func() { _ = client.CloseNow() }()
			require.Equal(t, 101, upgrade.StatusCode)
			for turn := 1; turn <= 2; turn++ {
				require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)))
				terminals := 0
				for {
					_, frame, readErr := client.Read(ctx)
					require.NoError(t, readErr)
					require.NotContains(t, string(frame), "response.completed")
					require.NotContains(t, string(frame), "stream ID")
					if gjson.GetBytes(frame, "type").String() == "response.failed" {
						terminals++
						code := gjson.GetBytes(frame, "response.error.code").String()
						if turn == 1 {
							require.Equal(t, "context_length_exceeded", code)
						} else {
							require.Equal(t, service.OpenAIUpstreamHTTP2StreamErrorCode, code)
						}
						break
					}
				}
				require.Equal(t, 1, terminals)
			}
			_, _, err = client.Read(ctx)
			require.Error(t, err)
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("middleware did not finish")
			}
			for len(opsErrorLogQueue) > 0 {
				flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
			}
			require.Equal(t, int32(2), submissions.Load(), "each submitted WS turn must be sent once")
			require.Len(t, repo.entries, 2, "final transport marker must preserve both WebSocket turn failures")
			require.NotEqual(t, "transport_error", repo.entries[0].ErrorType)
			require.Contains(t, repo.entries[0].ErrorBody, "context_length_exceeded")
			require.Equal(t, "transport_error", repo.entries[1].ErrorType)
			require.Equal(t, 101, repo.entries[1].StatusCode)
			require.Nil(t, repo.entries[1].UpstreamStatusCode)
		})
	}
}
