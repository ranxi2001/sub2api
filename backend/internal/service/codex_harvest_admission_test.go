package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func harvestProbeCompleteFixture(model string) string {
	return fmt.Sprintf("data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"model\":%q}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"model\":%q}}\n\n", model, model)
}

func TestCodex780AdmissionRequiresOriginalTicketContinuation(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		budget     int
	}{
		{"unchanged", "success", 2}, {"echo", "success", 2},
		{"reissued", "probe_reissued", 2}, {"route_changed", "probe_route_changed", 2},
		{"created_only", "response_incomplete_or_error", 2},
		{"late_error", "rate_limited", 2}, {"budget", "probe_inconclusive", 1},
		{"cancelled", "cancelled", 2}, {"mint_incomplete", "response_incomplete_or_error", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := ticketTestAccount(1)
			cfg := config.OpenAICodexTicketConfig{TargetLength: 780, TTLSeconds: 240}
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAICodexTicket: cfg}}}
			// Deliberately differs from the saved/default unified-88 setting.
			pair := mint780Pair(time.Now().Add(time.Hour), "unified-123")
			state := mint780State(time.Now().Add(-2 * time.Second))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, reserved := 0, 0
			svc.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, proxy string) (*http.Response, error) {
				calls++
				require.Equal(t, "http://127.0.0.1:17893", proxy)
				require.True(t, IsOpenAICodexStateProbeRequest(req.Context()), "must bypass gateway borrowing")
				require.Equal(t, "Bearer target-token", req.Header.Get("Authorization"))
				require.Equal(t, "same-session", req.Header.Get("session_id"))
				h := http.Header{}
				body := harvestProbeCompleteFixture("gpt-6-astra")
				if calls == 1 {
					require.Empty(t, req.Header.Get("Cookie"))
					require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
					h["Set-Cookie"] = pair
					h.Set(openAICodexTurnStateHeader, state)
					if tc.name == "mint_incomplete" {
						body = "data: {\"type\":\"response.created\"}\n\n"
					}
					if tc.name == "cancelled" {
						cancel()
					}
				} else {
					require.Equal(t, state, req.Header.Get(openAICodexTurnStateHeader))
					require.Equal(t, strings.Join(pair, "; "), req.Header.Get("Cookie"))
					switch tc.name {
					case "echo":
						h.Set(openAICodexTurnStateHeader, state)
					case "reissued":
						h.Set(openAICodexTurnStateHeader, mint780State(time.Now()))
					case "route_changed":
						h["Set-Cookie"] = mint780Pair(time.Now().Add(time.Hour), "unified-95")
					case "created_only":
						body = "data: {\"type\":\"response.created\"}\n\n"
					case "late_error":
						body += "data: {\"type\":\"error\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"secret\"}}\n\n"
					}
				}
				return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
			}}
			out := svc.executeCodexHarvestProbe(ctx, account, "target-token", "gpt-6-astra", "http://127.0.0.1:17893", time.Second, func() bool {
				if reserved >= tc.budget {
					return false
				}
				reserved++
				return true
			}, "same-session")
			require.Equal(t, tc.want, out.Kind)
			require.LessOrEqual(t, calls, tc.budget)
			require.NotContains(t, safeCodexHarvestError(out.Err), "secret")
			ticket := codexHarvestTicket(account, "gpt-6-astra", out, cfg, reserved)
			require.Equal(t, tc.want == "success", ticket.valid(time.Now(), 780))
			if tc.want == "success" {
				require.Equal(t, 2, calls)
				require.Equal(t, state, ticket.State)
				ticket.ProbeVerified = false
				require.False(t, ticket.valid(time.Now(), 780), "legacy shape-only tickets must be re-probed")
			}
		})
	}
}

func TestCodex780BusinessReplacementRequiresFreshProbe(t *testing.T) {
	for _, change := range []string{"state", "route"} {
		t.Run(change, func(t *testing.T) {
			account := ticketTestAccount(1)
			cfg := config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 780, TTLSeconds: 240, Models: []string{"gpt-6-astra"}}
			svc := ticketTestService(t, cfg, nil)
			now := time.Now().Add(-time.Second)
			state := mint780State(now)
			ticket := &openAICodexTicket{ProbeVerified: true, AccountID: 1, Model: "gpt-6-astra", State: state, Length: 780, Transport: "sse", Gateway: "unified-123", IssuedAt: now, ExpiresAt: now.Add(240 * time.Second), HarvestCookies: mint780Pair(now.Add(time.Hour), "unified-123")}
			require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), account, ticket))
			req, _ := http.NewRequest(http.MethodPost, chatgptCodexURL, nil)
			req.Header.Set(openAICodexTurnStateHeader, state)
			header := http.Header{}
			if change == "state" {
				header.Set(openAICodexTurnStateHeader, mint780State(time.Now()))
			} else {
				header["Set-Cookie"] = []string{"__cflb=changed", ticket.HarvestCookies[1]}
			}
			svc.observeCodexTicketResponse(req, &http.Response{StatusCode: 200, Header: header}, account)
			require.False(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra").valid(time.Now(), 780))
		})
	}
}
