//go:build unsentdelta

package service

import (
	"context"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestOpenAIWSUnsentDeltaDeliveryScope(t *testing.T) {
	for _, mutation := range []string{"valid", "pending", "failed_write", "terminal_excludes", "unknown_call", "changed_call", "wrong_pointer", "wrong_key", "wrong_model", "sent_error", "oversize"} {
		t.Run(mutation, func(t *testing.T) {
			before := openAIWSLocalHistoryBytes.Load()
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			manager, c, a := issue715WSManager(t, svc, context.Background())
			SetOpenAIWSRecoveryPrincipal(c, 715, 715)
			defer func() {
				manager.abort()
				require.Equal(t, before, openAIWSLocalHistoryBytes.Load(), "socket teardown must release retained history")
			}()
			source := `{"model":"gpt-6-astra","input":[{"role":"user","content":"fixture original task"}]}`
			require.NoError(t, issue715WSBegin(t, manager, a, source, "ctx_pool"))
			call := `{"type":"function_call","call_id":"call_fixture_delta","name":"fixture","arguments":"{}","status":"completed"}`
			if mutation == "pending" {
				require.NoError(t, manager.write([]byte(`{"type":"response.output_item.done","item":`+call+`}`), func() error { return nil }))
			} else {
				output := call
				if mutation == "terminal_excludes" {
					output = `{"type":"message","role":"assistant","content":"fixture complete without tool"}`
				}
				err := manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_fixture_delta","status":"completed","output":[`+output+`]}}`), func() error {
					if mutation == "failed_write" {
						return errors.New("fixture write failed")
					}
					return nil
				})
				if mutation == "failed_write" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			}
			current := `{"type":"response.create","model":"gpt-6-astra","previous_response_id":"resp_fixture_delta","input":[{"type":"function_call_output","call_id":"call_fixture_delta","output":"fixture executed once"}]}`
			model := "gpt-6-astra"
			switch mutation {
			case "unknown_call":
				current = strings.Replace(current, "call_fixture_delta", "call_unknown", 1)
			case "changed_call":
				manager.deliveredTools[model+"\x00call_fixture_delta"] = openAIWSDeliveredTool{fingerprint: "wrong", accountID: a.ID}
			case "wrong_pointer":
				current = strings.Replace(current, "resp_fixture_delta", "resp_foreign", 1)
			case "wrong_key":
				c.Set("api_key", &APIKey{ID: 999, GroupID: int64Ptr(715)})
			case "wrong_model":
				model = "gpt-6-sol"
			case "oversize":
				current = strings.Replace(current, "fixture executed once", strings.Repeat("x", openAIWSLocalHistoryLimit+1), 1)
			}
			cause := openAIWSUnsentTurn(denyOpenAITurn("account_ineligible"), []byte(current), model, 2)
			if mutation == "sent_error" {
				cause = errors.New("fixture unknown upstream delivery")
			}
			got := manager.restoreUnsentHistory(cause)
			var restored *openAIWSRestoredUnsentError
			if mutation == "valid" {
				require.ErrorAs(t, got, &restored)
				require.Contains(t, string(restored.body), "fixture original task")
				require.Contains(t, string(restored.body), "fixture executed once")
				require.NotContains(t, string(restored.body), `"type":"function_call"`)
				require.NotContains(t, string(restored.body), "previous_response_id")
				require.Same(t, cause, manager.restoreUnsentHistory(cause), "proof is consumed once")
			} else {
				require.Same(t, cause, got, "no evidence may be fabricated from failed delivery or foreign scope")
			}
		})
	}
}
