//go:build unit && bridgewarmup

package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"strings"
	"testing"
)

func TestHTTPBridgeAcceptedHistoryRequiresCompletedDelivery(t *testing.T) {
	const input = `{"model":"gpt-6-astra","store":false,"input":[{"type":"function_call","call_id":"old_call","name":"fixture","arguments":"{}","status":"completed"},{"type":"function_call_output","call_id":"old_call","output":"receipt"}]}`
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "completed"}[completed], func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			manager, _, account := issue715WSManager(t, svc, context.Background())
			defer manager.abort()
			require.NoError(t, issue715WSBegin(t, manager, account, input, "http_bridge"))
			event := `{"type":"response.failed","response":{"id":"resp_history","status":"failed","error":{"code":"fixture"},"output":[]}}`
			if completed {
				event = `{"type":"response.completed","response":{"id":"resp_history","status":"completed","output":[]}}`
			}
			require.NoError(t, manager.write([]byte(event), func() error { return nil }))
			call := gjson.Get(input, "input.0")
			require.Equal(t, completed, acceptsPriorToolPair(manager.acceptedHistoryTools, account, "gpt-6-astra", call, []byte(input)))
			require.False(t, acceptsPriorToolPair(manager.acceptedHistoryTools, account, "gpt-6-astra", call, []byte(strings.Replace(input, "receipt", "altered", 1))))
			require.False(t, acceptsPriorToolPair(manager.acceptedHistoryTools, account, "gpt-5.1", call, []byte(input)))
			other := *account
			other.ID++
			require.False(t, acceptsPriorToolPair(manager.acceptedHistoryTools, &other, "gpt-6-astra", call, []byte(input)))
			require.Empty(t, manager.deliveredTools, "accepted history must not masquerade as tools delivered by this generation")
		})
	}
}
