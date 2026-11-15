package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSPlaintextResponseOwnershipDelivery(t *testing.T) {
	for _, outcome := range []string{"completed", "empty_output", "done", "failed", "incomplete", "item_only", "write_failed", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			manager, _, account := issue715WSManager(t, svc, ctx)
			defer manager.abort()
			require.NoError(t, issue715WSBegin(t, manager, account, `{"model":"gpt-6-astra","input":"Use the fixture tool once"}`, "passthrough"))
			event := `{"type":"response.completed","response":{"id":"resp_fixture_plaintext","status":"completed","output":[{"type":"function_call","call_id":"call_fixture_plaintext","name":"fixture","arguments":"{}","status":"completed"}]}}`
			switch outcome {
			case "done":
				event = strings.Replace(event, "response.completed", "response.done", 1)
			case "empty_output":
				event = `{"type":"response.completed","response":{"id":"resp_fixture_plaintext","status":"completed","output":[]}}`
			case "failed", "incomplete":
				event = strings.ReplaceAll(strings.Replace(event, "response.completed", "response."+outcome, 1), `"status":"completed"`, `"status":"`+outcome+`"`)
			case "item_only":
				event = `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_fixture_plaintext","name":"fixture","arguments":"{}","status":"completed"}}`
			}
			err := manager.write([]byte(event), func() error {
				owner, lookupErr := svc.getOpenAIWSStateStore().GetResponseAccount(context.Background(), 715, "resp_fixture_plaintext")
				require.NoError(t, lookupErr)
				require.Zero(t, owner, "a terminal write in progress cannot publish response ownership")
				if outcome == "canceled" {
					cancel()
				}
				if outcome == "write_failed" {
					return errors.New("fixture failed delivery")
				}
				return nil
			})
			if outcome == "write_failed" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			manager.abort()
			owner, err := svc.getOpenAIWSStateStore().GetResponseAccount(context.Background(), 715, "resp_fixture_plaintext")
			require.NoError(t, err)
			if outcome == "completed" || outcome == "empty_output" || outcome == "done" {
				require.Equal(t, account.ID, owner, "PLAINTEXT_RESPONSE_OWNER_MISSING: delivered response must permit same-producer continuation")
			} else {
				require.Zero(t, owner, "unconfirmed delivery must not authorize continuation")
			}
			require.Empty(t, manager.request.outputs, "plaintext response must not create encrypted-item ownership")
		})
	}
}
