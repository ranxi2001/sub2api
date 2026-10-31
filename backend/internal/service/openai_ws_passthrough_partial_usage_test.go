package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// These assertions use only the defective head's public result contract. RED
// must fail on the missing current-turn result, never compilation/scaffolding.
func TestSmartProtocolPassthroughPartialUsage(t *testing.T) {
	for _, failure := range []string{"p1_eof", "p1_cancel", "p1_drain", "p1_control"} {
		for _, prior := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/prior_%t", failure, prior), func(t *testing.T) {
				f := newSmartProtocolNetwork(t, "ws", true, failure)
				f.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
				f.cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModePassthrough
				f.account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModePassthrough
				c := f.dial()
				if prior {
					f.send(c, "completed", "", 100)
					f.terminal(c)
					result := f.result()
					require.Equal(t, 7, result.Usage.OutputTokens)
				}
				f.send(c, "partial", "", 100)
				for {
					_, raw, err := c.Read(f.ctx)
					require.NoError(t, err)
					if gjson.GetBytes(raw, "type").String() == "response.in_progress" {
						break
					}
				}
				switch failure {
				case "p1_cancel":
					_ = c.CloseNow()
					close(f.partialRelease)
				case "p1_drain":
					_ = c.CloseNow()
				case "p1_control":
					f.cancel()
				}
				select {
				case <-f.errors:
				case <-time.After(6 * time.Second):
					t.Fatal("P1_LIFECYCLE_TIMEOUT")
				}
				var result *OpenAIForwardResult
				select {
				case result = <-f.results:
				default:
				}
				require.NotNil(t, result, "P1_CURRENT_TURN_USAGE_LOST")
				require.True(t, result.ObservedUsageOnError, "P1_OBSERVED_USAGE_NOT_SETTLED")
				require.False(t, result.UsageUnavailable)
				require.Equal(t, 3, result.Usage.InputTokens, "P1_PRIOR_TURN_INPUT_REBILLED")
				require.Equal(t, 2, result.Usage.OutputTokens, "P1_PRIOR_TURN_OUTPUT_REBILLED")
				require.Equal(t, "resp_partial", result.RequestID)
				require.Empty(t, result.UpstreamTerminalEvent, "transport failure must not invent a terminal")
				select {
				case <-f.results:
					t.Fatal("P1_DUPLICATE_TURN_RESULT")
				default:
				}
				want := int32(1)
				if prior {
					want = 2
				}
				require.Equal(t, want, f.wsCalls.Load(), "P1_UPSTREAM_REPLAY")
				require.Zero(t, f.httpCalls.Load())
			})
		}
	}
}
