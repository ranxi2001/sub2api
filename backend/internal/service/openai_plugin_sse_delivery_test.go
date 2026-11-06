package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type pluginDeliveryWriter struct {
	*httptest.ResponseRecorder
	start time.Time
	first time.Duration
	seen  bool
}

func (w *pluginDeliveryWriter) Flush() {
	w.ResponseRecorder.Flush()
	if !w.seen && strings.Contains(w.Body.String(), "data:") {
		w.first, w.seen = time.Since(w.start), true
	}
}

func TestPluginSSEProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, key, signature, accountType string
		enabled, mark, want               bool
	}{
		{"trusted", openAIWSSSEAcceleratorPluginKey, PluginSignatureTrusted, AccountTypeOAuth, true, true, true},
		{"forged_header", openAIWSSSEAcceleratorPluginKey, PluginSignatureTrusted, AccountTypeOAuth, true, false, false},
		{"unsigned", openAIWSSSEAcceleratorPluginKey, "unsigned", AccountTypeOAuth, true, true, false},
		{"wrong_plugin", "other", PluginSignatureTrusted, AccountTypeOAuth, true, true, false},
		{"api_key", openAIWSSSEAcceleratorPluginKey, PluginSignatureTrusted, AccountTypeAPIKey, true, true, false},
		{"flag_off", openAIWSSSEAcceleratorPluginKey, PluginSignatureTrusted, AccountTypeOAuth, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", nil)
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Sub2api-Plugin-Preflight-Sse": {"1"}}, Request: req}
			if tc.mark {
				markOpenAIPluginSSEPreflight(resp, req, &PluginInstallation{PluginKey: tc.key, SignatureStatus: tc.signature})
			}
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAIPluginSSEEarlyEventsEnabled: tc.enabled}}}
			require.Equal(t, tc.want, svc.openAIPluginSSEEarlyEvents(resp, &Account{Platform: PlatformOpenAI, Type: tc.accountType}))
		})
	}
}

func TestPluginSSEProvenanceResponseRequirements(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, declaration string
		status                         int
		want                           bool
	}{
		{"parameters", "text/event-stream; charset=utf-8", "1", http.StatusOK, true},
		{"case_insensitive_mime", "Text/Event-Stream", "1", http.StatusOK, true},
		{"wrong_status", "text/event-stream", "1", http.StatusCreated, false},
		{"wrong_mime", "application/json", "1", http.StatusOK, false},
		{"missing_declaration", "text/event-stream", "", http.StatusOK, false},
		{"wrong_declaration", "text/event-stream", "true", http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			resp := &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {tc.contentType}, "X-Sub2api-Plugin-Preflight-Sse": {tc.declaration}}}
			markOpenAIPluginSSEPreflight(resp, req, &PluginInstallation{PluginKey: openAIWSSSEAcceleratorPluginKey, SignatureStatus: PluginSignatureTrusted})
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAIPluginSSEEarlyEventsEnabled: true}}}
			require.Equal(t, tc.want, svc.openAIPluginSSEEarlyEvents(resp, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}))
			require.False(t, svc.openAIPluginSSEEarlyEvents(resp, &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}))
		})
	}
}

// A metadata payload can be parsed before its empty-line delimiter. Only a
// real boundary grants early delivery; an unfinished following event must not
// be spliced into the synthesized error after the stream has been committed.
func TestPluginSSEMetadataBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"forward", "passthrough"} {
		for _, scenario := range []string{"no_blank", "partial_json", "fragmented_boundary", "after_created_eof", "after_created_read_error", "after_created_timeout", "after_created_limit", "after_text_eof", "after_text_read_error"} {
			t.Run(path+"/"+scenario, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					start := time.Now()
					collector := requesttiming.New(start, 0)
					ctx := requesttiming.With(context.Background(), collector)
					w := &pluginDeliveryWriter{ResponseRecorder: httptest.NewRecorder(), start: start}
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
					c.Request, _ = requesttiming.StartAttempt(c.Request, 1, 0)
					ctx = c.Request.Context()
					reader, writer := io.Pipe()
					writerDone := make(chan struct{})
					defer func() { _ = reader.Close(); <-writerDone }()
					go func() {
						defer close(writerDone)
						defer func() { _ = writer.Close() }()
						time.Sleep(100 * time.Millisecond)
						created := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_boundary\"}}\n"
						switch scenario {
						case "no_blank":
							_, _ = io.WriteString(writer, created)
							return
						case "partial_json":
							_, _ = io.WriteString(writer, "event: response.created\ndata: {\"type\":\"response.created\",")
							return
						case "fragmented_boundary":
							if _, err := io.WriteString(writer, created); err != nil {
								return
							}
							time.Sleep(200 * time.Millisecond)
							if _, err := io.WriteString(writer, "\n"); err != nil {
								return
							}
							time.Sleep(200 * time.Millisecond)
							_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n"+
								"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n")
							return
						default:
							if _, err := io.WriteString(writer, created+"\n"); err != nil {
								return
							}
							time.Sleep(100 * time.Millisecond)
							if strings.HasPrefix(scenario, "after_text_") {
								if _, err := io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n"); err != nil {
									return
								}
								time.Sleep(100 * time.Millisecond)
							}
							if _, err := io.WriteString(writer, "event: response.in_progress\ndata: {\"type\":\"response.in_progress\",\"unfinished\":"); err != nil {
								return
							}
							switch scenario {
							case "after_created_limit":
								// Many bounded lines exceed the aggregate event limit,
								// rather than testing the scanner's separate line limit.
								_, _ = io.WriteString(writer, "\n"+strings.Repeat(":"+strings.Repeat("x", 32*1024)+"\n", openAIFirstOutputStageMaxBytes/(32*1024)+1))
							case "after_created_read_error", "after_text_read_error":
								_ = writer.CloseWithError(errors.New("fixture partial-event read failure"))
							case "after_created_timeout":
								time.Sleep(2 * time.Second)
							}
						}
					}()
					resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Sub2api-Plugin-Preflight-Sse": {"1"}}, Body: reader, Request: c.Request}
					markOpenAIPluginSSEPreflight(resp, c.Request, &PluginInstallation{PluginKey: openAIWSSSEAcceleratorPluginKey, SignatureStatus: PluginSignatureTrusted})
					svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAIPluginSSEEarlyEventsEnabled: true, MaxLineSize: defaultMaxLineSize}}}
					if scenario == "after_created_timeout" {
						svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
					}
					account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
					var err error
					if path == "forward" {
						_, err = svc.handleStreamingResponse(ctx, resp, c, account, start, "model", "model")
					} else {
						_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, account, start, "model", "model")
					}
					collector.Finish(http.StatusOK, false)
					var snapshot requesttiming.Snapshot
					collector.WhenFinished(func(s requesttiming.Snapshot) { snapshot = s })
					switch scenario {
					case "no_blank", "partial_json":
						require.Error(t, err)
						require.False(t, IsResponseCommitted(c))
						require.Empty(t, w.Body.String())
						require.NotContains(t, snapshot.Events, "first_sse_event_flush")
					case "fragmented_boundary":
						require.NoError(t, err)
						require.Equal(t, 300*time.Millisecond, w.first)
						require.Equal(t, float64(300), snapshot.Events["first_sse_event_flush"])
					default:
						require.Error(t, err)
						var failover *UpstreamFailoverError
						require.False(t, errors.As(err, &failover))
						require.True(t, IsResponseCommitted(c))
						require.NotContains(t, w.Body.String(), "unfinished")
						require.Equal(t, 1, strings.Count(w.Body.String(), "event: error")+strings.Count(w.Body.String(), `"type":"response.failed"`))
						for _, line := range strings.Split(w.Body.String(), "\n") {
							if payload, ok := extractOpenAISSEDataLine(line); ok {
								require.True(t, json.Valid([]byte(payload)), "synthesized failure must remain a complete JSON event")
							}
						}
					}
				})
			})
		}
	}
}

// Exercise both real host parsers with a scheduled upstream. The metadata
// commit must not disarm the output deadline or invent semantic/text timing.
func TestPluginSSEEarlyDelivery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous, _ := gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings)
	t.Cleanup(func() { gatewayForwardingCache.Store(previous) })
	for _, path := range []string{"forward", "passthrough"} {
		for _, scenario := range []string{"enabled", "flag_off", "multiline", "eof", "read_error", "timeout", "failed", "disconnect"} {
			t.Run(path+"/"+scenario, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					start := time.Now()
					gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{openAITTFTMode: OpenAITTFTModeSemantic, expiresAt: start.Add(time.Minute).UnixNano()})
					collector := requesttiming.New(start, 0)
					ctx := requesttiming.With(context.Background(), collector)
					w := &pluginDeliveryWriter{ResponseRecorder: httptest.NewRecorder(), start: start}
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
					c.Request, _ = requesttiming.StartAttempt(c.Request, 1, 0)
					ctx = c.Request.Context()
					if scenario == "disconnect" {
						c.Writer = &passthroughFlushTestWriter{ResponseWriter: c.Writer, recorder: w.ResponseRecorder, failAfterWrites: 1}
					}
					reader, writer := io.Pipe()
					writerDone := make(chan struct{})
					defer func() { _ = reader.Close(); <-writerDone }()
					go func() {
						defer close(writerDone)
						defer func() { _ = writer.Close() }()
						time.Sleep(100 * time.Millisecond)
						created := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fixture\"}}\n\n"
						if scenario == "multiline" {
							created = "event: response.created\ndata: {\ndata: \"response\":{\"id\":\"resp_fixture\"}}\n\n"
						}
						if _, err := io.WriteString(writer, created); err != nil {
							return
						}
						if scenario == "eof" {
							return
						}
						if scenario == "read_error" {
							_ = writer.CloseWithError(errors.New("fixture read failure"))
							return
						}
						if scenario == "timeout" {
							time.Sleep(2 * time.Second)
							return
						}
						if scenario == "failed" {
							_, _ = io.WriteString(writer, "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_fixture\",\"error\":{\"code\":\"server_error\",\"message\":\"overloaded\"}}}\n\n")
							return
						}
						time.Sleep(200 * time.Millisecond)
						_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\"}}\n\n")
						time.Sleep(500 * time.Millisecond)
						_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n")
						time.Sleep(200 * time.Millisecond)
						_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n")
					}()
					resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Sub2api-Plugin-Preflight-Sse": {"1"}}, Body: reader, Request: c.Request}
					markOpenAIPluginSSEPreflight(resp, c.Request, &PluginInstallation{PluginKey: openAIWSSSEAcceleratorPluginKey, SignatureStatus: PluginSignatureTrusted})
					svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAIPluginSSEEarlyEventsEnabled: scenario != "flag_off", MaxLineSize: defaultMaxLineSize}}}
					if scenario == "timeout" {
						svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
					}
					account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
					var err error
					var usage *OpenAIUsage
					if path == "forward" {
						r, e := svc.handleStreamingResponse(ctx, resp, c, account, start, "model", "model")
						err = e
						if r != nil {
							usage = r.usage
						}
					} else {
						r, e := svc.handleStreamingResponsePassthrough(ctx, resp, c, account, start, "model", "model")
						err = e
						if r != nil {
							usage = r.usage
						}
					}
					collector.Finish(200, false)
					var snapshot requesttiming.Snapshot
					collector.WhenFinished(func(s requesttiming.Snapshot) { snapshot = s })
					switch scenario {
					case "eof", "read_error", "timeout", "failed":
						require.Error(t, err)
						var failover *UpstreamFailoverError
						require.False(t, errors.As(err, &failover), "metadata committed; no cross-account replay")
						require.True(t, IsResponseCommitted(c))
						require.Equal(t, 1, strings.Count(w.Body.String(), "event: error")+strings.Count(w.Body.String(), `"type":"response.failed"`))
						require.NotContains(t, snapshot.Events, "first_semantic")
					default:
						require.NoError(t, err)
						require.NotNil(t, usage)
						require.Equal(t, 2, usage.InputTokens, "disconnect must still drain billing usage")
						require.InDelta(t, 300, snapshot.Events["first_semantic"], 1)
						require.InDelta(t, 800, snapshot.Attempts[0].Events["first_visible_text"], 1)
					}
					if scenario != "disconnect" {
						require.True(t, w.seen)
						if scenario == "flag_off" {
							require.GreaterOrEqual(t, w.first, 300*time.Millisecond)
						} else {
							require.Equal(t, 100*time.Millisecond, w.first)
							require.InDelta(t, 100, snapshot.Events["first_sse_event_flush"], 1)
						}
					}
				})
			})
		}
	}
}

func TestPluginSSEDeliveryIgnoresHeartbeatsAndIncompleteEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		collector := requesttiming.New(start, 0)
		ctx := requesttiming.With(context.Background(), collector)
		var o openAISSEDeliveryObservation
		o.data([]byte(`{}`), "heartbeat")
		o.boundary()
		o.flushed(ctx)
		o.data([]byte(`{"type":"response.created"}`), "response.created")
		o.flushed(ctx)
		time.Sleep(50 * time.Millisecond)
		o.boundary()
		time.Sleep(50 * time.Millisecond)
		o.flushed(ctx)
		collector.Finish(200, false)
		collector.WhenFinished(func(s requesttiming.Snapshot) {
			require.Equal(t, float64(100), s.Events["first_sse_event_flush"])
			require.NotContains(t, s.Events, "first_semantic")
		})
	})
}

func TestPluginSSEMetadataEligibilityRequiresOneCompletePayload(t *testing.T) {
	for _, tc := range []struct {
		name     string
		payloads [][]byte
		want     bool
	}{
		{"complete", [][]byte{[]byte(`{"type":"response.created"}`)}, true},
		{"multiline", [][]byte{nil, []byte(`{"type":"response.created"}`)}, true},
		{"trailing_data", [][]byte{[]byte(`{"type":"response.created"}`), nil}, false},
		{"unknown", [][]byte{[]byte(`{"type":"future.event"}`)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var event openAIPluginMetadataEvent
			for _, payload := range tc.payloads {
				event.data(payload, effectiveOpenAISSEEventType(payload, ""))
			}
			require.Equal(t, tc.want, event.boundary())
			require.False(t, event.boundary(), "classification must not leak into the next event")
		})
	}
}
