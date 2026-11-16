//go:build unsentdelta

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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
			if mutation == "pending" || mutation == "terminal_excludes" {
				delivered := false
				require.NoError(t, manager.write([]byte(`{"type":"response.output_item.done","item":`+call+`}`), func() error {
					delivered = true
					return nil
				}))
				require.True(t, delivered, "the tool item must reach the client before its terminal verdict")
			}
			if mutation != "pending" {
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

func TestOpenAIWSUnsentDeltaInstructions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		second string
		third  string
	}{
		{"omitted_after_omitted_turn", "", ""},
		{"omitted_after_explicit_turn", `,"instructions":"fixture turn two only"`, ""},
		{"current_null", "", `,"instructions":null`},
		{"current_explicit", "", `,"instructions":"fixture current control"`},
		{"current_empty", "", `,"instructions":""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := openAIWSLocalHistoryBytes.Load()
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			manager, c, account := issue715WSManager(t, svc, context.Background())
			SetOpenAIWSRecoveryPrincipal(c, 715, 715)
			defer func() {
				manager.abort()
				require.Equal(t, before, openAIWSLocalHistoryBytes.Load())
			}()
			first := `{"model":"gpt-6-astra","instructions":"fixture turn one only","tools":[{"type":"function","name":"fixture","parameters":{"type":"object"}}],"input":"fixture original task"}`
			require.NoError(t, issue715WSBegin(t, manager, account, first, "ctx_pool"))
			require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_instructions_1","status":"completed","output":[{"type":"message","role":"assistant","content":"fixture first answer"}]}}`), func() error { return nil }))
			second := `{"model":"gpt-6-astra","previous_response_id":"resp_instructions_1","input":"fixture second task"` + tc.second + `}`
			require.NoError(t, issue715WSBegin(t, manager, account, second, "ctx_pool"))
			require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_instructions_2","status":"completed","output":[{"type":"message","role":"assistant","content":"fixture second answer"}]}}`), func() error { return nil }))
			third := `{"type":"response.create","model":"gpt-6-astra","previous_response_id":"resp_instructions_2","input":"fixture current task"` + tc.third + `}`
			cause := openAIWSUnsentTurn(denyOpenAITurn("account_ineligible"), []byte(third), "gpt-6-astra", 3)
			var restored *openAIWSRestoredUnsentError
			require.ErrorAs(t, manager.restoreUnsentHistory(cause), &restored)
			want, got := gjson.Get(third, "instructions"), gjson.GetBytes(restored.body, "instructions")
			require.Equal(t, want.Exists(), got.Exists(), "STALE_INSTRUCTIONS_RESTORED: preserve current-frame field presence")
			require.Equal(t, want.Raw, got.Raw, "STALE_INSTRUCTIONS_RESTORED: preserve current-frame null or explicit value")
			require.Equal(t, gjson.Get(first, "tools").Raw, gjson.GetBytes(restored.body, "tools").Raw, "socket tool declarations still survive takeover")
			for _, text := range []string{"fixture original task", "fixture first answer", "fixture second task", "fixture second answer", "fixture current task"} {
				require.Contains(t, string(restored.body), text)
			}
			require.NotContains(t, string(restored.body), "previous_response_id")
			require.Same(t, cause, manager.restoreUnsentHistory(cause), "restoration proof is consumed once")
		})
	}
}

func TestOpenAIWSUnsentDeltaContinuationAnchor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
		previous string
		want     bool
	}{
		{"missing_anchor_independent_task", "", "", false},
		{"empty_anchor_independent_task", `"id":"",`, `,"previous_response_id":""`, false},
		{"null_anchor_independent_task", `"id":null,`, `,"previous_response_id":null`, false},
		{"valid_anchor_independent_task", `"id":"resp_anchor",`, "", false},
		{"valid_anchor_empty_previous", `"id":"resp_anchor",`, `,"previous_response_id":""`, false},
		{"valid_anchor_null_previous", `"id":"resp_anchor",`, `,"previous_response_id":null`, false},
		{"valid_anchor_blank_previous", `"id":"resp_anchor",`, `,"previous_response_id":"  "`, false},
		{"valid_anchor_wrong_previous", `"id":"resp_anchor",`, `,"previous_response_id":"resp_foreign"`, false},
		{"completed_matching_anchor", `"id":"resp_anchor",`, `,"previous_response_id":"resp_anchor"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := openAIWSLocalHistoryBytes.Load()
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			manager, c, account := issue715WSManager(t, svc, context.Background())
			SetOpenAIWSRecoveryPrincipal(c, 715, 715)
			defer func() {
				manager.abort()
				require.Equal(t, before, openAIWSLocalHistoryBytes.Load())
			}()
			require.NoError(t, issue715WSBegin(t, manager, account, `{"model":"gpt-6-astra","input":"fixture older independent task"}`, "ctx_pool"))
			// A successful plaintext terminal can be delivered without a response
			// ID. That delivery must not authorize attachment to another task.
			require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{`+tc.response+`"status":"completed","output":[{"type":"message","role":"assistant","content":"fixture older answer"}]}}`), func() error { return nil }))
			current := `{"type":"response.create","model":"gpt-6-astra","input":"fixture new task"` + tc.previous + `}`
			cause := openAIWSUnsentTurn(denyOpenAITurn("account_ineligible"), []byte(current), "gpt-6-astra", 2)
			got := manager.restoreUnsentHistory(cause)
			if !tc.want {
				require.Same(t, cause, got, "INDEPENDENT_TASK_ATTACHED: only an explicit nonempty matching completed anchor authorizes history")
				body, _, _, ok := OpenAIWSUnsentTurnPayload(got)
				require.True(t, ok)
				require.JSONEq(t, current, string(body))
				require.NotContains(t, string(body), "fixture older")
				if tc.response == "" || tc.response == `"id":"",` || tc.response == `"id":null,` {
					require.False(t, manager.localHistory.ready, "a terminal without an anchor cannot retain ready history")
				}
				return
			}
			var restored *openAIWSRestoredUnsentError
			require.ErrorAs(t, got, &restored)
			require.Contains(t, string(restored.body), "fixture older independent task")
			require.Contains(t, string(restored.body), "fixture older answer")
			require.Contains(t, string(restored.body), "fixture new task")
			require.NotContains(t, string(restored.body), "previous_response_id")
			require.Same(t, cause, manager.restoreUnsentHistory(cause))
		})
	}
}
