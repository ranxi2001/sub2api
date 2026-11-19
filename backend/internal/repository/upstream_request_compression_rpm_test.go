package repository

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Exercise real Forward -> RPM admission -> native HTTP -> compression. Only
// persistence and the destination socket are local fixtures; the production
// request builder, send checks and transport wrappers all run unchanged.
func TestHTTPUpstreamCompressionRejectionCannotBypassOpenAIRPM(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := upstreamstats.Default
	upstreamstats.Default = upstreamstats.NewStore(100, time.Now())
	defer func() { upstreamstats.Default = original }()
	var sends atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "chatgpt.com" || r.URL.Path != "/backend-api/codex/responses" {
			t.Error("the real Codex request builder must choose the first-party destination")
		}
		wire, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if sends.Add(1) == 1 {
			if r.Header.Get("Content-Encoding") != "zstd" {
				t.Error("the initial request must exercise default Codex compression")
			}
		} else if r.Header.Get("Content-Encoding") != "" || !strings.Contains(string(wire), strings.Repeat("rpm prompt ", 512)) {
			t.Error("the next admitted request must use the intact plain prompt")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Accept-Encoding", "identity")
		w.WriteHeader(http.StatusUnsupportedMediaType)
		_, _ = io.WriteString(w, `{"error":{"message":"unsupported request encoding","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(server.Close)
	redisServer := miniredis.RunT(t)
	// Keep the counter's clock fixed, away from a real minute boundary.
	minute := time.Now().Add(time.Hour).Truncate(time.Minute)
	redisServer.SetTime(minute)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	rpm := NewRPMCache(redisClient)
	cfg := &config.Config{}
	upstream, ok := NewHTTPUpstream(cfg).(*httpUpstreamService)
	require.True(t, ok)
	account := &service.Account{
		ID: 90007, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"access_token": "synthetic-token", "chatgpt_account_id": "synthetic-account"},
		Extra:       map[string]any{"base_rpm": 1},
	}
	entry, err := upstream.acquireClientWithProfile("", account.ID, 1, service.HTTPUpstreamProfileOpenAI)
	require.NoError(t, err)
	atomic.AddInt64(&entry.inFlight, -1)
	trusted, ok := server.Client().Transport.(*http.Transport)
	require.True(t, ok)
	transport := trusted.Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "chatgpt.com:443" {
			return nil, fmt.Errorf("unexpected fixture destination %q", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	entry.client = &http.Client{Transport: transport}
	t.Cleanup(transport.CloseIdleConnections)
	gateway := service.NewOpenAIGatewayService(
		&lifecycleAccountRepo{account: account}, nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, nil, upstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
		service.WithOpenAIRPMCache(rpm),
	)
	t.Cleanup(gateway.StopOpenAICodexTicketHarvester)
	body := []byte(`{"model":"gpt-5.1","stream":true,"input":"` + strings.Repeat("rpm prompt ", 512) + `"}`)
	forward := func(ctx context.Context) error {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body))).WithContext(ctx)
		_, err := gateway.Forward(ctx, c, account, body)
		return err
	}
	admit := func() context.Context {
		allowed, state, err := gateway.TryAcquireOpenAIOAuthRPM(t.Context(), account)
		require.NoError(t, err)
		require.True(t, allowed)
		require.Equal(t, 1, state.Current)
		return service.WithOpenAIRPMReservation(t.Context(), account, state)
	}
	since := time.Now()
	require.Error(t, forward(admit()), "the 415 must reach normal service error handling")
	require.EqualValues(t, 1, sends.Load(), "base_rpm=1 must never allow a hidden second POST")
	count, err := rpm.GetRPM(t.Context(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	observed := upstreamstats.Default.Snapshot([]int64{account.ID}, since, time.Now())[account.ID]
	require.EqualValues(t, 1, observed.Total)
	require.EqualValues(t, 1, observed.Failures)
	require.ErrorIs(t, forward(t.Context()), service.ErrOpenAIRPMExhausted, "cooldown cannot grant new send capacity")
	require.EqualValues(t, 1, sends.Load())
	require.Equal(t, observed, upstreamstats.Default.Snapshot([]int64{account.ID}, since, time.Now())[account.ID], "local RPM rejection must not create a physical response sample")
	redisServer.SetTime(minute.Add(time.Minute))
	require.Error(t, forward(admit()), "a fresh admitted request still owns its own upstream rejection")
	require.EqualValues(t, 2, sends.Load())
	count, err = rpm.GetRPM(t.Context(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 1, count, "the later independent request must acquire the new window's slot")
	observed = upstreamstats.Default.Snapshot([]int64{account.ID}, since, time.Now())[account.ID]
	require.EqualValues(t, 2, observed.Total)
	require.EqualValues(t, 2, observed.Failures)
	requireNoUpstreamInFlight(t, upstream)
}
