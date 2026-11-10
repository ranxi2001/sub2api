package service

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQualityOAuth401DirectSourceExcludesTicketsAndPlugins(t *testing.T) {
	for _, scenario := range []string{"native", "borrowed-ticket", "ticket-fail-open", "handled-plugin"} {
		t.Run(scenario, func(t *testing.T) {
			svc, account, ticket := accountTestTicketFixture(t)
			gateway := svc.openaiGatewayService
			if scenario != "borrowed-ticket" && scenario != "ticket-fail-open" {
				svc.openaiGatewayService = nil
			} else if scenario == "borrowed-ticket" {
				ticket.Acquisition, ticket.SourceAccountID = "reuse", account.ID+1
				require.NoError(t, gateway.storeOpenAICodexTicket(t.Context(), account, ticket))
			}
			if scenario == "ticket-fail-open" {
				gateway.openaiCodexTickets.Delete(openAICodexTicketKey(account.ID, "gpt-6-astra"))
				account.Extra = nil
				gateway.cfg.Gateway.OpenAICodexTicket.FailClosed = false
			}
			if scenario == "handled-plugin" {
				manager := &PluginManager{}
				manager.route.Store(&pluginRoute{pluginID: 1, rolloutPercent: 100, unavailable: "synthetic"})
				svc.pluginManager = manager
			}
			upstream := &accountTicketUpstream{do: func(*http.Request, string) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":"synthetic"}`))}, nil
			}}
			svc.httpUpstream, gateway.httpUpstream = upstream, upstream
			direct, native := false, false
			ctx, _ := newTestContext()
			ctx.Request = ctx.Request.WithContext(withPelicanTestOptions(ctx.Request.Context(), pelicanTestOptions{directOAuth401: &direct, nativeOAuthTransport: &native}))
			require.Error(t, svc.testOpenAIAccountConnection(ctx, account, "gpt-6-astra", "", AccountTestModeDefault))
			require.Equal(t, scenario == "native" || scenario == "ticket-fail-open", direct)
		})
	}
}

func TestQualityOAuth401EvidenceRequiresCompleteOwnCredentialRound(t *testing.T) {
	plan := qualityAnswerRetryPlan()
	plan.PelicanConfig.ModelIDs = []string{"sol", "astra"}
	plan.PelicanConfig.ParallelCount = 2
	makeResults := func() []*ScheduledTestResult {
		out := make([]*ScheduledTestResult, 4)
		for i := range out {
			out[i] = &ScheduledTestResult{Status: "failed", ErrorMessage: "API returned 401: synthetic", DirectOAuth401: true}
		}
		return out
	}
	require.Equal(t, &QualityOAuth401Round{AllDirect401: true, All401OrUnavailable: true}, QualityOAuth401Evidence(plan, makeResults()))
	for _, scenario := range []string{"partial", "nil", "judge", "timeout", "untrusted-prefix", "success", "probe"} {
		t.Run(scenario, func(t *testing.T) {
			results := makeResults()
			switch scenario {
			case "partial":
				results = results[:3]
			case "nil":
				results[0] = nil
			case "judge":
				results[0].ErrorMessage = "judge_inconclusive"
			case "timeout":
				results[0].ErrorMessage = "context deadline exceeded"
			case "untrusted-prefix":
				results[0].DirectOAuth401 = false
			case "success":
				results[0].Status = "success"
			case "probe":
				results[0].ErrorMessage = "state_probe_inconclusive: account_error"
			}
			require.Equal(t, &QualityOAuth401Round{}, QualityOAuth401Evidence(plan, results))
		})
	}
	results := makeResults()
	results[1] = &ScheduledTestResult{Status: "skipped", ErrorMessage: "model_unavailable"}
	require.Equal(t, &QualityOAuth401Round{All401OrUnavailable: true}, QualityOAuth401Evidence(plan, results))
	plan.PelicanConfig.TestChannel = "bps"
	require.Equal(t, &QualityOAuth401Round{}, QualityOAuth401Evidence(plan, makeResults()))
	// No caller can manufacture direct-source evidence in an API result body.
	var result ScheduledTestResult
	require.NoError(t, json.Unmarshal([]byte(`{"DirectOAuth401":true,"direct_oauth_401":true}`), &result))
	require.False(t, result.DirectOAuth401)
}

func TestQualityOAuth401PolicyLimitsAndReusableConfig(t *testing.T) {
	for _, maxRounds := range []int{-1, 0, 1, 6, 100, 101} {
		plan := qualityAnswerRetryPlan()
		plan.PelicanConfig.Quality.OAuth401MaxRounds = maxRounds
		_, err := nextPlanRun(plan, time.Now())
		if maxRounds < 0 || maxRounds > 100 {
			require.ErrorContains(t, err, "0-100")
		} else {
			require.NoError(t, err)
			copy, err := reusableQualityConfig(plan.PelicanConfig)
			require.NoError(t, err)
			require.Equal(t, maxRounds, copy.Quality.OAuth401MaxRounds)
		}
	}
	var legacy PelicanTestConfig
	require.NoError(t, json.Unmarshal([]byte(`{"quality":{"action":"observe_only"}}`), &legacy))
	require.Zero(t, legacy.Quality.OAuth401MaxRounds)
}
