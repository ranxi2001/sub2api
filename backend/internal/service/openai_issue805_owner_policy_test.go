package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestIssue805PolicySeparatesRoutingFromReplaySafety(t *testing.T) {
	for _, known := range []bool{false, true} {
		for _, disabled := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "unknown", true: "known"}[known], map[bool]string{false: "strict", true: "relaxed"}[disabled]}, "/"), func(t *testing.T) {
				svc := &OpenAIGatewayService{cfg: &config.Config{}}
				a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
				if known {
					issue715LearnRoute(t, svc, a, issue715OpaqueBody)
				}
				svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = disabled
				c, _ := newTurnStateTestContext(t, 715, "fixture-session")
				c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
				body := strings.TrimSuffix(issue715OpaqueBody, "]}") + `,{"type":"function_call_output","call_id":"fixture","output":"already applied"}]}`
				ctx := svc.WithOpenAIWSAgentRecovery(context.Background(), c, []byte(body), "fixture-session", "gpt-6-astra")
				if known || disabled {
					require.True(t, OpenAIAgentToolResultContinuation(ctx), "removing binding cannot authorize tool-result replay")
					require.False(t, OpenAIWSAgentRecoveryCanRetry(ctx), "unknown opaque input is not evidence for replay")
				}
				b := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
				req, err := http.NewRequest(http.MethodPost, "https://other.example/responses", bytes.NewBufferString(body))
				require.NoError(t, err)
				finish, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, b, req)
				if known && !disabled {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				finish(true)
				if known && disabled {
					for _, binding := range svc.agentMessageRecovery.bindings {
						require.EqualValues(t, 1, binding.accountID)
					}
				}
			})
		}
	}
}

func TestIssue805AlternativeStillNeedsNormalAdmission(t *testing.T) {
	for _, restriction := range []string{"none", "group", "model", "group_model", "quota", "disabled", "excluded", "concurrency"} {
		t.Run(restriction, func(t *testing.T) {
			accounts := []Account{
				{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{715}},
				{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{715}, Credentials: map[string]any{}, Concurrency: 1},
			}
			// The real repository's group query only returns group members.
			// Preserve that contract instead of using the intentionally broad
			// generic scheduler stub for the group-revocation case.
			svc := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}}
			issue715LearnRoute(t, svc, &accounts[0], issue715OpaqueBody)
			svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
			reset := time.Now().Add(time.Hour)
			accounts[0].RateLimitResetAt = &reset
			excluded := map[int64]struct{}{}
			switch restriction {
			case "group":
				accounts[1].GroupIDs = []int64{716}
			case "model":
				accounts[1].Credentials["model_mapping"] = map[string]any{"other-model": "other-model"}
			case "group_model":
				accounts[1].AccountGroups = []AccountGroup{{GroupID: 715, AllowedModels: []string{"other-model"}}}
			case "quota":
				accounts[1].Extra = map[string]any{"quota_limit": float64(10), "quota_used": float64(10)}
			case "disabled":
				accounts[1].Schedulable = false
			case "excluded":
				excluded[2] = struct{}{}
			case "concurrency":
				svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{2: false}})
			}
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(issue715OpaqueBody), "fixture-session", "gpt-6-astra")
			selected, _, err := svc.SelectAccountWithScheduler(ctx, int64Ptr(715), "", "fixture-session", "gpt-6-astra", excluded, OpenAIUpstreamTransportAny, false)
			if selected != nil && selected.ReleaseFunc != nil {
				defer selected.ReleaseFunc()
			}
			if restriction == "none" {
				require.NoError(t, err)
				require.NotNil(t, selected)
				require.EqualValues(t, 2, selected.Account.ID)
				return
			}
			if restriction == "concurrency" && selected != nil {
				require.False(t, selected.Acquired, "opt-out cannot grant an occupied slot")
				require.NotNil(t, selected.WaitPlan)
				return
			}
			require.Error(t, err)
			require.Nil(t, selected)
		})
	}
}

func TestIssue805FinalAdmissionStillRechecksGroup(t *testing.T) {
	accounts := []Account{{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{715}}}
	repo := &turnAdmissionRepo{account: &accounts[0]}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: repo, requireLatestTurnAdmission: true}
	svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
	selected := accounts[0]
	_, err := svc.admitOpenAITurnForGroup(context.Background(), 715, true, &selected, "gpt-6-astra")
	require.NoError(t, err, "the originally selected account must qualify before revocation")
	accounts[0].GroupIDs = []int64{716}
	_, err = svc.admitOpenAITurnForGroup(context.Background(), 715, true, &selected, "gpt-6-astra")
	var denied *OpenAITurnAdmissionError
	require.ErrorAs(t, err, &denied)
	require.Equal(t, "group_membership_changed", denied.Reason, "opt-out cannot bypass final fresh admission")
	require.Equal(t, 2, repo.reads, "both sends must use the production authoritative admission contract")
}

func TestIssue805FinalSendPreservesHistoryAndDeliveryFences(t *testing.T) {
	for _, mutation := range []string{"intact", "tool_result", "user_history", "agent", "pending", "failed", "conflict", "lost", "canceled"} {
		t.Run(mutation, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			issue715LearnRoute(t, svc, a, issue715OpaqueBody)
			svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			body := strings.TrimSuffix(issue715OpaqueBody, "]}") + `,{"type":"message","role":"user","content":"original user history"},{"type":"function_call_output","call_id":"fixture","output":"already applied"}]}`
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := svc.WithOpenAIAgentMessageRecovery(parent, c, []byte(body), "fixture-session", "gpt-6-astra")
			switch mutation {
			case "tool_result":
				body = strings.Replace(body, "already applied", "changed", 1)
			case "user_history":
				body = strings.Replace(body, "original user history", "changed", 1)
			case "agent":
				body = strings.Replace(body, "fixture-opaque-reference", "changed", 1)
			case "canceled":
				cancel()
			default:
				for key, binding := range svc.agentMessageRecovery.bindings {
					switch mutation {
					case "pending":
						binding.pending = "pending:fixture"
					case "failed":
						binding.failed = true
					case "conflict":
						binding.conflicted = true
					case "lost":
						delete(svc.agentMessageRecovery.bindings, key)
						continue
					}
					svc.agentMessageRecovery.bindings[key] = binding
				}
			}
			req, err := http.NewRequest(http.MethodPost, "https://other.example/responses", bytes.NewBufferString(body))
			require.NoError(t, err)
			finish, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, req)
			if mutation == "intact" {
				require.NoError(t, err)
				finish(true)
			} else {
				require.Error(t, err, "policy must not disable the final safety check")
			}
		})
	}
}

func TestIssue805WSPointerAndRecordLossStayConservative(t *testing.T) {
	for _, mutation := range []string{"foreign_pointer", "unknown_pointer", "pointer_only", "record_loss", "write_error", "cancel"} {
		t.Run(mutation, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			manager, _, account := issue715WSManager(t, svc, parent)
			defer manager.abort()
			require.NoError(t, issue715WSBegin(t, manager, account, `{"model":"gpt-6-astra","input":[]}`, "ctx_pool"))
			item := `{"type":"agent_message","encrypted_content":"fixture-805"}`
			terminal := `{"type":"response.completed","response":{"id":"resp_805_owner","status":"completed","output":[` + item + `]}}`
			err := manager.write([]byte(terminal), func() error {
				if mutation == "cancel" {
					cancel()
				}
				if mutation == "write_error" {
					return errors.New("fixture downstream failure")
				}
				return nil
			})
			if mutation == "write_error" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			body := `{"model":"gpt-6-astra","input":[` + item + `]}`
			account.ID = 2
			switch mutation {
			case "foreign_pointer", "pointer_only":
				body = strings.Replace(body, `"input":`, `"previous_response_id":"resp_805_owner","input":`, 1)
				if mutation == "pointer_only" {
					body = strings.Replace(body, item, "", 1)
				}
			case "unknown_pointer":
				body = strings.Replace(body, `"input":`, `"previous_response_id":"resp_unknown","input":`, 1)
			case "record_loss":
				clear(svc.agentMessageRecovery.bindings)
			}
			require.Error(t, issue715WSBegin(t, manager, account, body, "ctx_pool"), "neither a server pointer nor uncertain delivery becomes portable")
			require.False(t, OpenAIWSAgentRecoveryCanRetry(manager.ctx))
		})
	}
}

func TestIssue805WSFirstSeenHistoryDoesNotInventOwner(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
	manager, _, account := issue715WSManager(t, svc, context.Background())
	defer manager.abort()
	require.NoError(t, issue715WSBegin(t, manager, account, issue715OpaqueBody, "ctx_pool"))
	require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_805_first_seen","status":"completed","output":[]}}`), func() error { return nil }))
	require.Empty(t, svc.agentMessageRecovery.bindings, "consumption of unknown history is not a producer observation")
	require.Empty(t, manager.known, "later explicit turns must not confuse an unpublished input with lost records")
	require.False(t, OpenAIWSAgentRecoveryCanRetry(manager.ctx), "lack of an owner is never permission to automatically replay opaque work")
	account.ID = 2
	require.NoError(t, issue715WSBegin(t, manager, account, issue715OpaqueBody, "ctx_pool"), "fresh first-seen history follows ordinary admission without inferring a forgotten owner")
}

func TestIssue805WSRefusedCurrentPointerCannotReplayWarmup(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
	manager, _, account := issue715WSManager(t, svc, context.Background())
	defer manager.abort()
	require.True(t, OpenAIWSAgentRecoveryCanRetry(manager.ctx), "empty warmup has no prior opaque side effect")
	err := issue715WSBegin(t, manager, account, `{"model":"gpt-6-astra","previous_response_id":"resp_unknown","input":[]}`, "ctx_pool")
	require.Error(t, err)
	require.False(t, OpenAIWSAgentRecoveryCanRetry(manager.ctx), "a refused current pointer cannot be replaced by replaying the initial warmup")
}
