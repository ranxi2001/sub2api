package handler

import (
	"context"
	"encoding/json"
	"strings"
	"fmt"
	"net/http"
	"net/http/httptest"
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
						if namespace != "oauth" {
							return
						}
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
