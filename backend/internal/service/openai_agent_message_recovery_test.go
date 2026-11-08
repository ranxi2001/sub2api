package service

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const issue715OpaqueBody = `{"model":"gpt-6-astra","input":[{"type":"agent_message","author":"fixture-parent","recipient":"fixture-child","content":[{"type":"input_text","text":"fixture envelope"},{"type":"encrypted_content","encrypted_content":"fixture-opaque-reference"}]}]}`

func issue715LearnRoute(t *testing.T, svc *OpenAIGatewayService, account *Account, body string) {
	t.Helper()
	c, _ := newTurnStateTestContext(t, 715, "fixture-session")
	c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
	ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(body), "fixture-session", "gpt-6-astra")
	req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer fixture")
	complete, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req, []byte(body))
	require.NoError(t, err)
	complete(true)
}

func TestIssue715RecoveryScopeAndPayloadIdentity(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}}
	issue715LearnRoute(t, svc, account, issue715OpaqueBody)
	for _, scope := range []string{"same", "key", "group", "session", "model", "task", "reasoning", "top_level_agent"} {
		t.Run(scope, func(t *testing.T) {
			key, group := int64(715), int64(715)
			session, model, body := "fixture-session", "gpt-6-astra", issue715OpaqueBody
			switch scope {
			case "key":
				key++
			case "group":
				group++
			case "session":
				session += "-other"
			case "model":
				model = "other-model"
			case "task":
				body = string(bytes.ReplaceAll([]byte(body), []byte("fixture-opaque-reference"), []byte("different-task")))
			case "reasoning":
				body = `{"input":[{"type":"reasoning","encrypted_content":"fixture-opaque-reference"}]}`
			case "top_level_agent":
				body = `{"input":[{"type":"agent_message","encrypted_content":"fixture-opaque-reference"}]}`
			}
			c, _ := newTurnStateTestContext(t, key, session)
			c.Set("api_key", &APIKey{ID: key, GroupID: &group})
			ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(body), session, model)
			require.Equal(t, scope == "same", openAIAgentRecoveryBound(ctx), "compatibility evidence is exact and credential scoped")
		})
	}
}

func TestIssue715RecoveryDoesNotLearnUncompletedOrChangedWire(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	c, _ := newTurnStateTestContext(t, 715, "fixture-session")
	c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
	ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(issue715OpaqueBody), "fixture-session", "gpt-6-astra")
	req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewBufferString(issue715OpaqueBody))
	require.NoError(t, err)
	complete, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req, []byte(issue715OpaqueBody))
	require.NoError(t, err)
	complete(false)
	require.Empty(t, svc.agentMessageRecovery.bindings, "HTTP 200, EOF, cancellation and incomplete are not completion evidence")
	req, err = http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewBufferString(`{"input":[]}`))
	require.NoError(t, err)
	complete, err = svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req, []byte(issue715OpaqueBody))
	require.NoError(t, err)
	complete(true)
	require.Empty(t, svc.agentMessageRecovery.bindings, "a transformed-away task must never earn a compatibility grant")
}

func TestIssue715RecoveryRejectsAmbiguousTask(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	c, _ := newTurnStateTestContext(t, 715, "fixture-session")
	for _, body := range []string{
		`{"input":[{"type":"agent_message","encrypted_content":"first","encrypted_content":"second"}]}`,
		`{"input":[{"type":"agent_message","encrypted_content":"first"}],"input":[]}`,
	} {
		ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(body), "fixture-session", "gpt-6-astra")
		require.True(t, openAIAgentRecoveryFromContext(ctx).conflict)
	}
}

func TestIssue715CompletionEvidenceIsUnambiguous(t *testing.T) {
	for _, sample := range []struct {
		raw              string
		nested, accepted bool
	}{
		{`{"status":"completed","error":null}`, false, true},
		{`{"status":"completed"}`, false, true},
		{`{"status":"incomplete","error":null}`, false, false},
		{`{"status":"completed","error":{"code":"failure"}}`, false, false},
		{`{"status":"completed","status":"failed","error":null}`, false, false},
		{`{"response":{"status":"completed","error":null}}`, true, true},
		{`{"response":{"status":"completed","error":null,"error":{"code":"failure"}}}`, true, false},
	} {
		require.Equal(t, sample.accepted, openAIAgentCompletedResponse([]byte(sample.raw), sample.nested))
	}
}

func TestIssue715RecoveryIgnoresUnrelatedCredentialMetadata(t *testing.T) {
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture", "model_mapping": map[string]any{"gpt-6-astra": "gpt-6-astra"}}}
	before := openAIAgentRecoveryConfig(account, "gpt-6-astra")
	account.Credentials["plan_type"] = "fixture-plan"
	account.Credentials["expires_at"] = "fixture-expiry"
	account.Credentials["model_mapping"].(map[string]any)["unrelated"] = "other"
	require.Equal(t, before, openAIAgentRecoveryConfig(account, "gpt-6-astra"))
	account.Credentials["api_key"] = "fixture-rotated-identity"
	require.NotEqual(t, before, openAIAgentRecoveryConfig(account, "gpt-6-astra"))
}

func TestIssue715RecoveryConflictingConcurrentCompletionAndExpiry(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	c, _ := newTurnStateTestContext(t, 715, "fixture-session")
	c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
	ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(issue715OpaqueBody), "fixture-session", "gpt-6-astra")
	var finishes []func(bool)
	for _, id := range []int64{1, 2} {
		account := &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
		req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewBufferString(issue715OpaqueBody))
		require.NoError(t, err)
		complete, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req, []byte(issue715OpaqueBody))
		require.NoError(t, err)
		finishes = append(finishes, complete)
	}
	var workers sync.WaitGroup
	for _, finish := range finishes {
		workers.Add(1)
		go func(f func(bool)) { defer workers.Done(); f(true) }(finish)
	}
	workers.Wait()
	next := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(issue715OpaqueBody), "fixture-session", "gpt-6-astra")
	require.True(t, openAIAgentRecoveryFromContext(next).conflict, "concurrent conflicting owners must not become last-writer-wins")
	_, _, err := svc.SelectAccountWithScheduler(next, int64Ptr(715), "", "fixture-session", "gpt-6-astra", nil, OpenAIUpstreamTransportAny, false)
	require.ErrorIs(t, err, errOpenAIAgentRecoveryUnavailable)
	for key, binding := range svc.agentMessageRecovery.bindings {
		binding.expiresAt = time.Now().Add(-time.Minute)
		svc.agentMessageRecovery.bindings[key] = binding
	}
	next = svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(issue715OpaqueBody), "fixture-session", "gpt-6-astra")
	require.False(t, openAIAgentRecoveryBound(next), "simulated expiry cannot manufacture compatibility")
}

func TestIssue715RecoveryRetainsNativeOwnerAgainstBPSPreference(t *testing.T) {
	for _, advanced := range []string{"true", "false"} {
		t.Run(advanced, func(t *testing.T) {
			native := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{715}}
			bps := *excelAccount()
			bps.ID, bps.Priority, bps.GroupIDs = 2, -1, []int64{715}
			cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:fixture-session": 2}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{native, bps}}, cache: cache, rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(advanced)}
			issue715LearnRoute(t, svc, &native, issue715OpaqueBody)
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(issue715OpaqueBody), "fixture-session", "gpt-6-astra")
			selection, _, err := svc.SelectAccountWithScheduler(ctx, int64Ptr(715), "", "fixture-session", "gpt-6-astra", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, int64(1), selection.Account.ID, "failed BPS sticky must not replace successful native evidence")
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			selection, _, err = svc.SelectAccountWithScheduler(ctx, int64Ptr(715), "", "fixture-session", "gpt-6-astra", map[int64]struct{}{1: {}}, OpenAIUpstreamTransportAny, false)
			require.Error(t, err)
			require.Nil(t, selection, "excluded owner must not fall through to a guessed compatible route")
		})
	}
}
