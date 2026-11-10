//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	hcplugin "github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type quality401PluginClient struct {
	pluginv1.TransportPluginClient
}

func (*quality401PluginClient) Forward(ctx context.Context, _ ...grpc.CallOption) (pluginv1.TransportPlugin_ForwardClient, error) {
	return &quality401PluginStream{ctx: ctx, frames: []*pluginv1.ForwardResponse{
		{Frame: &pluginv1.ForwardResponse_Start{Start: &pluginv1.ForwardResponseStart{StatusCode: http.StatusUnauthorized}}},
		{Frame: &pluginv1.ForwardResponse_BodyChunk{BodyChunk: []byte(`{"error":{"code":"token_invalidated"}}`)}},
		{Frame: &pluginv1.ForwardResponse_End{End: &pluginv1.ForwardResponseEnd{}}},
	}}, nil
}

type quality401PluginStream struct {
	pluginv1.TransportPlugin_ForwardClient
	ctx    context.Context
	frames []*pluginv1.ForwardResponse
}

func (s *quality401PluginStream) Context() context.Context {
	return s.ctx
}
func (*quality401PluginStream) Send(*pluginv1.ForwardRequest) error {
	return nil
}
func (*quality401PluginStream) CloseSend() error {
	return nil
}
func (s *quality401PluginStream) Recv() (*pluginv1.ForwardResponse, error) {
	if len(s.frames) == 0 {
		return nil, io.EOF
	}
	frame := s.frames[0]
	s.frames = s.frames[1:]
	return frame, nil
}

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
				manager.route.Store(&pluginRoute{pluginID: 1, rolloutPercent: 100,
					runtime: &pluginRuntime{client: &hcplugin.Client{}, api: &quality401PluginClient{}}})
				svc.pluginManager = manager
			}
			repo := &openAIAccountTestRepo{}
			svc.accountRepo = repo
			upstream := &accountTicketUpstream{do: func(*http.Request, string) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"token_invalidated"}}`))}, nil
			}}
			svc.httpUpstream, gateway.httpUpstream = upstream, upstream
			direct, revoked, native := false, false, false
			ctx, _ := newTestContext()
			ctx.Request = ctx.Request.WithContext(withPelicanTestOptions(ctx.Request.Context(), pelicanTestOptions{directOAuth401: &direct, directOAuthRevoked401: &revoked, nativeOAuthTransport: &native}))
			require.Error(t, svc.testOpenAIAccountConnection(ctx, account, "gpt-6-astra", "", AccountTestModeDefault))
			require.Equal(t, scenario == "native" || scenario == "ticket-fail-open", direct)
			require.Equal(t, direct, revoked)
			require.Equal(t, account.ID, repo.setErrorID)
			require.Contains(t, repo.setErrorMsg, "token_invalidated", "generic error text persists even on untrusted transports")
		})
	}
}

func TestQualityOAuth401RevocationRequiresStructuredCode(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"not invalid_grant; token not revoked"}}`,
		`{"error":{"code":"other","message":"token_invalidated"}}`,
		`{"error":{"message":"{\"error\":{\"code\":\"invalid_grant\"}}"}}`,
		`{"error":{"code":"not_invalid_grant"}}`,
		`invalid_grant`,
	} {
		require.False(t, qualityOAuth401RevokedCode([]byte(body)), body)
	}
	for _, code := range []string{"token_revoked", "token_invalidated", "refresh_token_invalidated", "refresh_token_reused", "invalid_grant"} {
		require.True(t, qualityOAuth401RevokedCode([]byte(`{"error":{"code":"`+code+`"}}`)), code)
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
	for _, scenario := range []string{"partial", "nil", "judge", "timeout", "untrusted-prefix", "success", "probe", "wrong-answer", "catalog-unknown", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			results := makeResults()
			results[0].DirectOAuthRevoked401 = true
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
			case "wrong-answer":
				results[0].ErrorMessage = "answer_mismatch"
			case "catalog-unknown":
				results[0] = &ScheduledTestResult{Status: "skipped", ErrorMessage: "model_catalog_unavailable"}
			case "unsupported":
				results[0] = &ScheduledTestResult{Status: "skipped", ErrorMessage: "unsupported_model"}
			}
			require.Equal(t, &QualityOAuth401Round{}, QualityOAuth401Evidence(plan, results))
		})
	}
	results := makeResults()
	results[1] = &ScheduledTestResult{Status: "skipped", ErrorMessage: "model_unavailable"}
	require.Equal(t, &QualityOAuth401Round{All401OrUnavailable: true}, QualityOAuth401Evidence(plan, results))
	results[0].DirectOAuthRevoked401 = true
	require.Equal(t, &QualityOAuth401Round{All401OrUnavailable: true, HasDirectRevoked401: true}, QualityOAuth401Evidence(plan, results))
	plan.PelicanConfig.TestChannel = "bps"
	require.Equal(t, &QualityOAuth401Round{}, QualityOAuth401Evidence(plan, makeResults()))
	// No caller can manufacture direct-source evidence in an API result body.
	var result ScheduledTestResult
	require.NoError(t, json.Unmarshal([]byte(`{"DirectOAuth401":true,"direct_oauth_401":true,"DirectOAuthRevoked401":true,"direct_oauth_revoked_401":true}`), &result))
	require.False(t, result.DirectOAuth401)
	require.False(t, result.DirectOAuthRevoked401)
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
