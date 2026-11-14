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

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestModerationUpstreamHTTPSelectionAndFailover(t *testing.T) {
	for _, accountType := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			for _, scenario := range []string{"unselected", "deny", "failover-deny", "failover-allow", "group-mismatch", "empty-groups", "empty-accounts", "multi-group"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", accountType, stream, scenario), func(t *testing.T) {
					var audits, writesA, writesB atomic.Int32
					skipAudit := scenario == "unselected" || scenario == "group-mismatch" || scenario == "empty-groups" || scenario == "empty-accounts"
					audit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						audits.Add(1)
						body, _ := io.ReadAll(r.Body)
						require.Contains(t, string(body), "frozen original input")
						score := "0.99"
						if scenario == "failover-allow" {
							score = "0.01"
						}
						_, _ = fmt.Fprintf(w, `{"results":[{"category_scores":{"sexual":%s}}]}`, score)
					}))
					defer audit.Close()
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						isA := strings.Contains(r.Header.Get("Authorization"), "fixture-a")
						if isA {
							writesA.Add(1)
						} else {
							writesB.Add(1)
						}
						if isA && strings.HasPrefix(scenario, "failover") {
							w.WriteHeader(503)
							_, _ = w.Write([]byte(`{"error":{"message":"fixture overloaded"}}`))
							return
						}
						if stream || accountType == service.AccountTypeOAuth {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_scope\",\"model\":\"gpt-5.1\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
						} else {
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, `{"id":"resp_scope","status":"completed","model":"gpt-5.1","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
						}
					}))
					defer upstream.Close()
					accounts := []service.Account{}
					for i, token := range []string{"fixture-a", "fixture-b"} {
						accounts = append(accounts, service.Account{ID: int64(i + 1), Name: token, Platform: service.PlatformOpenAI, Type: accountType, Status: service.StatusActive, Schedulable: true, Priority: i, GroupIDs: []int64{3131, 3132}, Credentials: map[string]any{"api_key": token, "access_token": token, "base_url": "https://fixture.example"}, Extra: map[string]any{"codex_fingerprint_mode": "off"}})
					}
					target, err := url.Parse(upstream.URL)
					require.NoError(t, err)
					transport := moderationLoopbackUpstream{&nativeImageRouteUpstream{target: target, client: upstream.Client()}}
					h := newOpenAIResponsesFailoverTestHandler(t, transport, accounts...)
					ids := []int64{1}
					if scenario == "unselected" {
						ids = []int64{99}
					}
					if scenario == "failover-deny" {
						ids = []int64{2}
					}
					if scenario == "failover-allow" {
						ids = []int64{1, 2}
					}
					if scenario == "empty-accounts" {
						ids = nil
					}
					bindings := map[int64]service.ModerationUpstreamBinding{}
					for _, a := range accounts {
						bindings[a.ID] = service.ModerationUpstreamBinding{Platform: a.Platform, Type: a.Type, GroupIDs: a.GroupIDs}
					}
					policy := map[string]any{"enabled": true, "mode": "pre_block", "all_groups": true, "sample_rate": 100, "base_url": audit.URL, "api_keys": []string{"fixture-audit"}, "upstream_account_scope": "selected", "upstream_account_ids": ids, "upstream_account_bindings": bindings}
					if scenario == "group-mismatch" || scenario == "empty-groups" || scenario == "multi-group" {
						policy["all_groups"] = false
						policy["group_ids"] = []int64{3132}
						if scenario == "empty-groups" {
							policy["group_ids"] = []int64{}
						}
						if scenario == "multi-group" {
							policy["group_ids"] = []int64{3131, 3132}
						}
					}
					raw, err := json.Marshal(policy)
					require.NoError(t, err)
					repo := &contentModerationHandlerSettingRepo{values: map[string]string{service.SettingKeyRiskControlEnabled: "true", service.SettingKeyContentModerationConfig: string(raw)}}
					h.contentModerationService = service.NewContentModerationService(repo, &contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil, nil, openAIImagesFailoverAccountRepo{accounts: accounts})
					c, rec := newOpenAIResponsesFailoverTestContext(t, context.Background())
					c.Request.Body = io.NopCloser(strings.NewReader(fmt.Sprintf(`{"model":"gpt-5.1","stream":%t,"input":"frozen original input"}`, stream)))
					h.Responses(c)
					if scenario == "deny" || scenario == "multi-group" {
						require.Zero(t, h.gatewayService.SnapshotOpenAIAccountSchedulerMetrics().RuntimeStatsAccountCount, "local deny must not create an upstream health sample")
						require.Equal(t, 1, strings.Count(rec.Body.String(), `"error":`), "local deny emits exactly one error")
						require.True(t, json.Valid(rec.Body.Bytes()))
					}
					if skipAudit {
						require.Zero(t, audits.Load())
						require.Equal(t, int32(1), writesA.Load(), rec.Body.String())
						require.Contains(t, rec.Body.String(), "resp_scope")
					} else {
						require.Equal(t, int32(1), audits.Load(), "same logical content must be audited once across retry/failover")
						if scenario == "failover-allow" {
							require.Positive(t, writesA.Load(), rec.Body.String())
							require.Equal(t, int32(1), writesB.Load())
							require.Contains(t, rec.Body.String(), "resp_scope")
						} else {
							require.Zero(t, writesB.Load())
							require.Contains(t, rec.Body.String(), "content_policy_violation")
							if scenario == "deny" || scenario == "multi-group" {
								require.Zero(t, writesA.Load())
							} else {
								require.Positive(t, writesA.Load(), rec.Body.String())
							}
						}
					}
				})
			}
		}
	}
}
