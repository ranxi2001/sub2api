package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestModerationUpstreamWebSocketModesAndTurns(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge} {
		for _, namespace := range []string{"none", "oauth"} {
			for _, scenario := range []string{"unselected", "deny", "next-turn", "failover"} {
				if scenario == "failover" && namespace != "oauth" {
					continue
				}
				t.Run(mode+"/"+namespace+"/"+scenario, func(t *testing.T) {
					var audits atomic.Int32
					audit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						n := audits.Add(1)
						score := "0.99"
						if scenario == "next-turn" && n == 1 {
							score = "0.01"
						}
						_, _ = fmt.Fprintf(w, `{"results":[{"category_scores":{"sexual":%s}}]}`, score)
					}))
					defer audit.Close()
					accountType := service.AccountTypeAPIKey
					if namespace == "oauth" {
						accountType = service.AccountTypeOAuth
					}
					ids := []int64{9951}
					if scenario == "unselected" {
						ids = []int64{9953}
					}
					raw, err := json.Marshal(map[string]any{"enabled": true, "mode": "pre_block", "all_groups": true, "sample_rate": 100, "base_url": audit.URL, "api_keys": []string{"fixture"}, "upstream_account_scope": "selected", "upstream_account_ids": ids, "upstream_account_bindings": map[int64]service.ModerationUpstreamBinding{9951: {Platform: service.PlatformOpenAI, Type: accountType, GroupIDs: []int64{4301}}}})
					require.NoError(t, err)
					opts := issue600FixtureOptions{moderationSettings: map[string]string{service.SettingKeyContentModerationConfig: string(raw)}}
					if scenario == "failover" {
						opts.switchAccount = true
					}
					h, up := newIssue600NetworkHarness(t, mode, namespace, false, nil, opts)
					payload := `{"type":"response.create","model":"gpt-5.1","input":"synthetic scope first"}`
					if scenario == "unselected" {
						issue600Send(t, h, payload, 1, false)
						issue600Send(t, h, strings.ReplaceAll(payload, "first", "second"), 2, false)
						require.Zero(t, audits.Load())
					} else {
						if scenario == "next-turn" {
							issue600Send(t, h, payload, 1, false)
							payload = strings.ReplaceAll(payload, "first", "second")
						}
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(payload)))
						_, event, readErr := h.clientConn.Read(ctx)
						if readErr == nil {
							require.Equal(t, "error", gjson.GetBytes(event, "type").String())
							require.Contains(t, string(event), "content_policy_violation")
						} else {
							require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(readErr))
						}
						expected := int32(1)
						if scenario == "next-turn" {
							expected = 2
						}
						require.Equal(t, expected, audits.Load())
					}
					issue600Finish(t, h, nil)
					frames, _ := up.snapshot()
					expectedWrites := 0
					if scenario == "unselected" {
						expectedWrites = 2
					}
					if scenario == "next-turn" {
						expectedWrites = 1
					}
					require.Len(t, frames, expectedWrites, "selected deny must precede actual generation writes")
					if scenario == "failover" {
						rejected, _ := up.rejectedSnapshot()
						require.NotEmpty(t, rejected, "unselected first account must be tried before selected failover denial")
					}
				})
			}
		}
	}
}

func TestModerationUpstreamWebSocketTurnIdentityAfterFailover(t *testing.T) {
	var audits atomic.Int32
	audit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		audits.Add(1)
		body, _ := io.ReadAll(r.Body)
		score := "0.01"
		if strings.Contains(string(body), "blocked-third") {
			score = "0.99"
		}
		_, _ = fmt.Fprintf(w, `{"results":[{"category_scores":{"sexual":%s}}]}`, score)
	}))
	defer audit.Close()
	raw, err := json.Marshal(map[string]any{"enabled": true, "mode": "pre_block", "all_groups": true, "sample_rate": 100, "base_url": audit.URL, "api_keys": []string{"fixture"}, "upstream_account_scope": "selected", "upstream_account_ids": []int64{9951, 9952}, "upstream_account_bindings": map[int64]service.ModerationUpstreamBinding{9951: {Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, GroupIDs: []int64{4301}}, 9952: {Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, GroupIDs: []int64{4301}}}})
	require.NoError(t, err)
	h, up := newIssue600NetworkHarness(t, service.OpenAIWSIngressModeHTTPBridge, "oauth", false, nil, issue600FixtureOptions{switchAfterTurns: 1, moderationSettings: map[string]string{service.SettingKeyContentModerationConfig: string(raw)}})
	issue600Send(t, h, `{"type":"response.create","model":"gpt-5.1","input":"first"}`, 1, false)
	issue600Send(t, h, `{"type":"response.create","model":"gpt-5.1","input":"second"}`, 2, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"blocked-third"}`)))
	_, event, err := h.clientConn.Read(ctx)
	require.NoError(t, err)
	require.Contains(t, string(event), "content_policy_violation")
	issue600Finish(t, h, nil)
	frames, _ := up.snapshot()
	require.Len(t, frames, 2)
	require.Equal(t, int32(3), audits.Load(), "replay does not audit twice; genuinely new third turn must audit")
}

func TestModerationUpstreamWebSocketCurrentAliasControlsScope(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge} {
		t.Run(mode, func(t *testing.T) {
			var audits atomic.Int32
			audit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				audits.Add(1)
				body, _ := io.ReadAll(r.Body)
				require.Contains(t, string(body), "second blocked input")
				_, _ = w.Write([]byte(`{"results":[{"category_scores":{"sexual":0.99}}]}`))
			}))
			defer audit.Close()
			raw, err := json.Marshal(map[string]any{"enabled": true, "mode": "pre_block", "all_groups": true, "model_filter": map[string]any{"type": "include", "models": []string{"public-b"}}, "sample_rate": 100, "base_url": audit.URL, "api_keys": []string{"fixture"}, "upstream_account_scope": "selected", "upstream_account_ids": []int64{9951}, "upstream_account_bindings": map[int64]service.ModerationUpstreamBinding{9951: {Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, GroupIDs: []int64{4301}}}})
			require.NoError(t, err)
			h, up := newIssue600NetworkHarness(t, mode, "none", false, nil, issue600FixtureOptions{modelMapping: map[string]string{"public-a": "gpt-5.1", "public-b": "gpt-5.1"}, moderationSettings: map[string]string{service.SettingKeyContentModerationConfig: string(raw)}})
			issue600Send(t, h, `{"type":"response.create","model":"public-a","input":"first"}`, 1, false)
			require.Zero(t, audits.Load())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"public-b","input":"second blocked input"}`)))
			_, event, err := h.clientConn.Read(ctx)
			require.NoError(t, err)
			require.Contains(t, string(event), "content_policy_violation")
			issue600Finish(t, h, nil)
			frames, _ := up.snapshot()
			require.Len(t, frames, 1)
			require.Equal(t, int32(1), audits.Load())
		})
	}
}
