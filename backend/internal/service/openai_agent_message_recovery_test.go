package service

import (
	"bytes"
	"context"
	"errors"
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
	complete, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
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
	complete, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
	require.NoError(t, err)
	complete(false)
	require.Empty(t, svc.agentMessageRecovery.bindings, "HTTP 200, EOF, cancellation and incomplete are not completion evidence")
	req, err = http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewBufferString(`{"input":[]}`))
	require.NoError(t, err)
	complete, err = svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
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
	mapping, ok := account.Credentials["model_mapping"].(map[string]any)
	require.True(t, ok)
	mapping["unrelated"] = "other"
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
		complete, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
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

func TestIssue715RecoveryCannotBypassRPM(t *testing.T) {
	account := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{715}, Extra: map[string]any{"base_rpm": 1, "rpm_strategy": "sticky_exempt"}}
	acquired := []int64{}
	svc := &OpenAIGatewayService{
		accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{account}}, cfg: &config.Config{},
		rpmCache:           &openAIRPMTestCache{counts: map[int64]int{1: 1}},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquiredIDs: &acquired}),
	}
	issue715LearnRoute(t, svc, &account, issue715OpaqueBody)
	c, _ := newTurnStateTestContext(t, 715, "fixture-session")
	c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
	ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(issue715OpaqueBody), "fixture-session", "gpt-6-astra")
	selection, _, err := svc.SelectAccountWithScheduler(ctx, int64Ptr(715), "", "fixture-session", "gpt-6-astra", nil, OpenAIUpstreamTransportAny, false)
	require.Error(t, err)
	require.Nil(t, selection)
	require.Empty(t, acquired, "compatibility cannot bypass the hard per-minute admission gate")
}

type issue715SharedOwners struct {
	GatewayCache
	mu      sync.Mutex
	values  map[string]string
	readErr error
}

func (c *issue715SharedOwners) GetOpenAIAgentRecovery(ctx context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.values[key], c.readErr
}
func (c *issue715SharedOwners) PublishOpenAIAgentRecovery(ctx context.Context, key, value string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = make(map[string]string)
	}
	if old := c.values[key]; old != "" && old != value {
		value = "conflict"
	}
	c.values[key] = value
	return nil
}
func TestIssue715SharedOwnershipIsAuthoritativeAndMixedTasksRemainIntact(t *testing.T) {
	cache := &issue715SharedOwners{}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	producer := &OpenAIGatewayService{cache: cache, cfg: &config.Config{}}
	issue715LearnRoute(t, producer, account, issue715OpaqueBody)
	reader := &OpenAIGatewayService{cache: cache, cfg: &config.Config{}}
	c, _ := newTurnStateTestContext(t, 715, "fixture-session")
	c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
	mixed := issue715OpaqueBody[:len(issue715OpaqueBody)-2] + `,{"type":"agent_message","encrypted_content":"fixture-new-task"}]}`
	ctx := reader.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(mixed), "fixture-session", "gpt-6-astra")
	require.True(t, openAIAgentRecoveryBound(ctx))
	require.Len(t, openAIAgentRecoveryFromContext(ctx).keys, 2)
	req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewBufferString(mixed))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer fixture")
	complete, err := reader.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
	require.NoError(t, err, "unknown new task alongside proven history must remain sendable to that owner")
	complete(true)
	require.Len(t, cache.values, 2, "successful intact consumption can learn the additional new item")
	cache.readErr = errors.New("fixture Redis unavailable")
	ctx = reader.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(mixed), "fixture-session", "gpt-6-astra")
	var recovery *OpenAIAgentRecoveryError
	require.ErrorAs(t, openAIAgentRecoveryFromContext(ctx).lookupErr, &recovery)
	require.Equal(t, "store_unavailable", recovery.Reason)
	cache.readErr = nil
	cache.values = make(map[string]string)
	ctx = producer.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(issue715OpaqueBody), "fixture-session", "gpt-6-astra")
	require.False(t, openAIAgentRecoveryBound(ctx), "shared miss cannot resurrect process-local ownership")
}

func TestIssue715PlainRequestsWithoutSafeCaptureKeepOriginalBehavior(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	c, _ := newTurnStateTestContext(t, 715, "fixture-session")
	c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
	for _, body := range []string{`{"model":"gpt-6-astra","input":"fixture","metadata":{"x":1,"x":2}}`, `{"model":"gpt-6-astra","input":"fixture"}`} {
		ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(body), "fixture-session", "gpt-6-astra")
		req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewBufferString(body))
		require.NoError(t, err)
		if !bytes.Contains([]byte(body), []byte("metadata")) {
			req.GetBody = nil
		}
		complete, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
		require.NoError(t, err, "best effort output capture must not reject ordinary requests")
		complete(true)
		require.Empty(t, svc.agentMessageRecovery.bindings)
	}
}

func TestIssue715RecoveryIncludesActualWireHeaderCasing(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	c, _ := newTurnStateTestContext(t, 715, "fixture-session")
	c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
	req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewBufferString(issue715OpaqueBody))
	require.NoError(t, err)
	req.Header["openai-organization"] = []string{"fixture-org-one"}
	ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(issue715OpaqueBody), "fixture-session", "gpt-6-astra")
	complete, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
	require.NoError(t, err)
	complete(true)
	ctx = svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(issue715OpaqueBody), "fixture-session", "gpt-6-astra")
	req.Header["openai-organization"] = []string{"fixture-org-two"}
	_, err = svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
	var recovery *OpenAIAgentRecoveryError
	require.ErrorAs(t, err, &recovery, "a raw-cased wire identity change must not hide behind Header.Get")
	require.Equal(t, "wire_changed", recovery.Reason)
}

func TestIssue715ToolResultContinuationCoversCodexOutputTypes(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	issue715LearnRoute(t, svc, account, issue715OpaqueBody)
	c, _ := newTurnStateTestContext(t, 715, "fixture-session")
	c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
	for _, typ := range codexToolCallOutputItemTypes {
		body := issue715OpaqueBody[:len(issue715OpaqueBody)-2] + `,{"type":"` + typ + `","call_id":"fixture-call","output":"fixture already applied"}]}`
		ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(body), "fixture-session", "gpt-6-astra")
		require.True(t, OpenAIAgentToolResultContinuation(ctx), typ+" must stop ambiguous automatic replay")
	}
}
