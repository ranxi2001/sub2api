package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/mihomo"
	"github.com/stretchr/testify/require"
)

func TestCodex780RouteFailuresKeepShapeAndSafeReason(t *testing.T) {
	now := time.Now()
	pair := mint780Pair(now.Add(time.Hour), "unified-123")
	for _, tt := range []struct {
		name    string
		cookies []string
		want    string
	}{
		{"missing_pair", nil, "route_pair_missing"},
		{"missing_cflb", pair[1:], "route_cflb_missing"},
		{"missing_oailb", pair[:1], "route_oailb_missing"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAICodexTicket: config.OpenAICodexTicketConfig{TargetLength: 780}}}}
			svc.httpUpstream = &harvestProxyUpstream{do: func(*http.Request, string) (*http.Response, error) {
				h := http.Header{"Set-Cookie": tt.cookies}
				h.Set(openAICodexTurnStateHeader, mint780State(now))
				return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
			}}
			r := svc.executeCodexHarvestProbe(context.Background(), ticketTestAccount(1), "secret", "gpt-6-astra", "", time.Second, func() bool { return true }, "session")
			require.Equal(t, "invalid_route", r.Kind)
			require.EqualError(t, r.Err, tt.want)
			require.Len(t, r.State, 780)
			require.Equal(t, 33, r.Shape.Blocks)
		})
	}
}

func TestCodex780GatewayHostsAndAnyKeepActualRoute(t *testing.T) {
	for _, host := range []string{"198", "123", "141", "23", "15", "95", "162", "136", "168", "107", "200", "176", "83", "180", "97", "196"} {
		gateway := "unified-" + host
		full := "chat.gateway." + gateway + ".api.openai.com"
		require.Equal(t, gateway, normalizeCodex780Gateway(full))
		pair := mint780Pair(time.Now().Add(time.Hour), gateway)
		for _, target := range []string{full, "any"} {
			cookies, _, err := codex780Route(pair, target, time.Now())
			require.NoError(t, err)
			require.Equal(t, gateway, codex780CookieGateway(cookies))
		}
	}
	_, _, err := codex780Route(nil, "any", time.Now())
	require.Error(t, err)
	_, _, err = codex780Route(mint780Pair(time.Now().Add(-time.Second), "unified-123"), "any", time.Now())
	require.Error(t, err)
	for _, input := range []string{"any", "*", "chat.gateway.unified-123.api.openai.com"} {
		controls := CodexHarvestControls{Version: 1, TargetGateway: input, Speed: CodexHarvestSpeedPresets()["standard"]}
		require.NoError(t, ValidateCodexHarvestControls(controls))
	}
	for _, input := range []string{"chat.gateway.unified-123.api.openai.com.evil.test", "https://user:secret@host"} {
		require.Error(t, ValidateCodexHarvestControls(CodexHarvestControls{Version: 1, TargetGateway: input, Speed: CodexHarvestSpeedPresets()["standard"]}))
	}
	account := ticketTestAccount(1)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 780}, nil)
	svc.codexHarvest = &CodexHarvestService{current: CodexHarvestControls{Transport: "sse", TargetGateway: "any"}, loadedUntil: time.Now().Add(time.Hour)}
	svc.httpUpstream = &harvestProxyUpstream{do: func(*http.Request, string) (*http.Response, error) {
		h := http.Header{"Set-Cookie": mint780Pair(time.Now().Add(time.Hour), "unified-123")}
		h.Set(openAICodexTurnStateHeader, mint780State(time.Now()))
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(harvestProbeCompleteFixture("gpt-6-astra")))}, nil
	}}
	r := svc.executeCodexHarvestProbe(context.Background(), account, "secret", "gpt-6-astra", "", time.Second, func() bool { return true }, "session")
	require.Equal(t, "success", r.Kind)
	require.Equal(t, "unified-123", r.Gateway)
	ticket := codexHarvestTicket(account, "gpt-6-astra", r, svc.openAICodexTicketConfig(), 1)
	bindCodexHarvestEgress(ticket, codexHarvestAttempt{proxy: "http://127.0.0.1:17893", node: mihomo.HarvestNode{ID: "node-1", Name: "dynamic-1", Provider: "managed"}}, "session")
	require.Equal(t, mihomo.Endpoint, ticket.HarvestProxyURL)
	svc.openaiCodexTickets.Store(openAICodexTicketKey(1, "gpt-6-astra"), ticket)
	require.NotNil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
	svc.codexHarvest.current.TargetGateway = "unified-95"
	require.NotNil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"), "legacy host setting must not discard a probe-verified ticket")
}

func TestCodex780TransportErrorsAreUsefulAndRedacted(t *testing.T) {
	for _, tt := range []struct {
		err      error
		category string
	}{
		{context.DeadlineExceeded, "timeout"}, {&net.DNSError{Err: "secret", Name: "secret.example"}, "dns_error"},
		{syscall.ECONNREFUSED, "connection_refused"}, {io.EOF, "connection_closed"},
		{errors.New("https://user:secret@host token=secret"), "transport_error"},
	} {
		err := mintTransportError(&url.Error{Op: "Post", URL: "https://user:secret@host", Err: tt.err})
		require.EqualError(t, err, "mint transport: "+tt.category)
		require.NotContains(t, err.Error(), "secret")
	}
}

func TestCodex780SeedPairCannotMaskPartialRotation(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "partial_rotation"}[rotate], func(t *testing.T) {
			account := ticketTestAccount(1)
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 780}, nil)
			now := time.Now()
			pair := mint780Pair(now.Add(time.Hour), "unified-88")
			svc.openaiCodexTickets.Store(openAICodexTicketKey(1, "gpt-6-astra"), &openAICodexTicket{AccountID: 1, Model: "gpt-6-astra", State: mint780State(now), Length: 780, Transport: "sse", Gateway: "unified-88", IssuedAt: now, ExpiresAt: now.Add(time.Minute), HarvestCookies: pair})
			svc.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, _ string) (*http.Response, error) {
				if req.Header.Get(openAICodexTurnStateHeader) == "" {
					require.Empty(t, req.Header.Get("Cookie"))
				} else {
					require.Equal(t, strings.Join(pair, "; "), req.Header.Get("Cookie"))
				}
				h := http.Header{}
				h.Set(openAICodexTurnStateHeader, mint780State(now))
				if req.Header.Get(openAICodexTurnStateHeader) == "" {
					h["Set-Cookie"] = pair
				} else if rotate {
					h.Add("Set-Cookie", "__cflb=new-route")
				}
				return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(harvestProbeCompleteFixture("gpt-6-astra")))}, nil
			}}
			r := svc.executeCodexHarvestProbe(context.Background(), account, "secret", "gpt-6-astra", "", time.Second, func() bool { return true }, "new-session")
			if rotate {
				require.Equal(t, "invalid_route", r.Kind)
				require.EqualError(t, r.Err, "route_oailb_missing")
			} else {
				require.Equal(t, "success", r.Kind)
			}
		})
	}
}
