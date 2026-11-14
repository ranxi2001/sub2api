//go:build unit

package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const recoveredWSAuditCanary = "fixture-restored-unsafe-task-834"
const recoveredWSAuditNext = "fixture-new-turn-must-be-audited-834"
const recoveredWSAuditAgent = `{"type":"agent_message","author":"/root","recipient":"/root/worker","encrypted_content":"fixture-ws-audit-exact-task"}`

// Record the production evaluator's actual persistence arguments. The existing
// securityaudit integration suite separately verifies the PostgreSQL writes.
type recoveredWSAuditStore struct {
	securityaudit.JobRepository
	mu      sync.Mutex
	records []securityaudit.Event
}

func (r *recoveredWSAuditStore) RecordBlocking(_ context.Context, snapshot securityaudit.PromptSnapshot, _ int64, result *securityaudit.NormalizedResult, _ bool) (*securityaudit.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	event := securityaudit.Event{Snapshot: snapshot, ScannerEvidence: result.ScannerEvidence}
	r.records = append(r.records, event)
	return &event, nil
}

type recoveredWSAuditPrompt struct {
	armed     atomic.Bool
	deny      bool
	restored  atomic.Int32
	private   atomic.Bool
	group     atomic.Int64
	evaluator *securityaudit.GuardEvaluator
}

func (*recoveredWSAuditPrompt) EffectiveMode() securityaudit.Mode { return securityaudit.ModeBlocking }
func (*recoveredWSAuditPrompt) Enqueue(context.Context, securityaudit.Request) error {
	return errors.New("unexpected asynchronous audit")
}
func (p *recoveredWSAuditPrompt) Evaluate(ctx context.Context, req securityaudit.Request) (*securityaudit.PromptDecision, error) {
	if p.armed.Load() && strings.Contains(string(req.Body), recoveredWSAuditCanary) {
		p.restored.Add(1)
		p.private.Store(req.NoBodyRetention && service.HasRecoveredContextPrivacy(ctx))
		if req.GroupID != nil {
			p.group.Store(*req.GroupID)
		}
	}
	snapshot, err := securityaudit.ExtractBlockingPromptSnapshot(req, false)
	if errors.Is(err, securityaudit.ErrNoPromptText) {
		return &securityaudit.PromptDecision{Kind: securityaudit.DecisionAllow, AllowNextStage: true}, nil
	}
	if err != nil {
		return nil, err
	}
	return p.evaluator.Evaluate(ctx, securityaudit.ActiveConfig{
		Enabled: true, RiskControlEnabled: true, BlockingEnabled: true, StorePassEvents: true,
		GroupIDs: []int64{4301}, Scanners: []string{"jailbreak"}, ConfigVersion: 1,
		Endpoints: []securityaudit.ActiveEndpoint{{ID: "fixture", Enabled: true, TimeoutMS: 1000, InputLimit: 8192}},
	}, snapshot)
}
func (p *recoveredWSAuditPrompt) Scan(_ context.Context, _ securityaudit.ActiveEndpoint, text string, _ []string) (*securityaudit.NormalizedResult, error) {
	result := &securityaudit.NormalizedResult{
		Decision: securityaudit.EventPass, RiskLevel: securityaudit.RiskLow, Action: securityaudit.ActionAllow,
		Safety: "Safe", Categories: []string{}, MatchedScanners: []string{}, ScannerScores: map[string]float64{},
		ScannerEvidence: map[string]string{"fixture": text},
	}
	if p.armed.Load() && p.deny && strings.Contains(text, recoveredWSAuditCanary) {
		result.Decision, result.RiskLevel, result.Action = securityaudit.EventCritical, securityaudit.RiskCritical, securityaudit.ActionBlock
		result.Safety, result.Categories, result.MatchedScanners = "Unsafe", []string{"jailbreak"}, []string{"jailbreak"}
		result.ScannerScores["jailbreak"] = 1
	}
	return result, nil
}

func TestOpenAI409WSRecoveredAudit(t *testing.T) {
	for _, mode := range readableWSModes {
		for _, policy := range []string{"prompt", "selected"} {
			t.Run(mode+"/"+policy, func(t *testing.T) { runRecoveredWSAudit(t, mode, policy, false) })
		}
	}
}

func TestOpenAI409WSRecoveredAuditNextTurn(t *testing.T) {
	for _, mode := range readableWSModes {
		t.Run(mode, func(t *testing.T) { runRecoveredWSAudit(t, mode, "selected", true) })
	}
}

func runRecoveredWSAudit(t *testing.T, mode, policy string, allowRecovery bool) {
	t.Helper()
	clients := issue805Redis(t)
	probe := clients()
	store := &recoveredWSAuditStore{}
	prompt := &recoveredWSAuditPrompt{deny: policy == "prompt"}
	prompt.evaluator = securityaudit.NewGuardEvaluator(prompt, store, securityaudit.NewAtomicMetrics())
	var writesA, writesB, selectedRestored atomic.Int32
	var bBodyMu sync.Mutex
	var bBodies [][]byte
	answer := func(r *http.Request, body []byte) []byte {
		output := recoveredWSAuditAgent
		id := "resp_audit_A"
		if r.Header.Get("Authorization") == "Bearer fixture-audit-A" {
			writesA.Add(1)
		} else {
			writesB.Add(1)
			bBodyMu.Lock()
			bBodies = append(bBodies, append([]byte(nil), body...))
			bBodyMu.Unlock()
			if r.Header.Get("Authorization") != "Bearer fixture-audit-B" || !strings.Contains(string(body), recoveredWSAuditCanary) || !strings.Contains(string(body), "completed-audit-result") || strings.Contains(string(body), "fixture-ws-audit-exact-task") {
				return []byte(`{"type":"response.failed","response":{"status":"failed","error":{"code":"fixture_wrong_restored_task"}}}`)
			}
			id, output = "resp_audit_B", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"audit-fixture-B-received-restored"}]}`
		}
		return []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"model":"gpt-5.1","status":"completed","output":[%s],"usage":{"input_tokens":2,"output_tokens":1}}}`, id, output))
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", answer(r, body))
			return
		}
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow() //nolint:errcheck // Fixture teardown.
		for {
			_, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), coderws.MessageText, answer(r, body)); err != nil {
				return
			}
		}
	}))
	defer provider.Close()
	audit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		text := string(body)
		if strings.Contains(text, recoveredWSAuditCanary) {
			selectedRestored.Add(1)
		}
		score := "0.01"
		if (!allowRecovery && strings.Contains(text, recoveredWSAuditCanary)) || strings.Contains(text, recoveredWSAuditNext) {
			score = "0.99"
		}
		_, _ = fmt.Fprintf(w, `{"results":[{"category_scores":{"sexual":%s}}]}`, score)
	}))
	defer audit.Close()
	settings := map[string]string{service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true", service.SettingKeyCyberSessionIdentityStrictEnabled: "true"}
	if policy == "selected" {
		settings[service.SettingKeyContentModerationConfig] = fmt.Sprintf(`{"enabled":true,"mode":"pre_block","all_groups":false,"group_ids":[4301],"sample_rate":100,"record_non_hits":true,"base_url":%q,"api_keys":["fixture"],"upstream_account_scope":"selected","upstream_account_ids":[2],"upstream_account_bindings":{"2":{"platform":"openai","type":"apikey","group_ids":[4301]}}}`, audit.URL)
	}
	headers := http.Header{"Session_id": {"fixture-audit-session"}, "Thread-Id": {"fixture-audit-thread"}, "Conversation_id": {"fixture-audit-thread"}}
	var accounts *readableWSAccounts
	h := newOpenAIWSPassthroughHandlerHarnessWithAudit(t, provider.URL, headers, settings, repository.NewGatewayCache(clients()),
		func(key *service.APIKey) { key.AllowLongContext = true },
		func(h *OpenAIGatewayHandler) {
			h.securityAuditCoordinator = securityaudit.NewCoordinator(securityaudit.NewLegacyModerationAdapter(h.contentModerationService), prompt)
		},
		func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, repo *service.AccountRepository) {
			a.ID, a.Priority = 1, 0
			a.Credentials["api_key"] = "fixture-audit-A"
			a.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			a.Extra["codex_fingerprint_mode"] = "off"
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 15
			cfg.Gateway.MaxAccountSwitches = 2
			cfg.Gateway.Scheduling.OpenAITurnAdmissionCacheTTLSeconds = 0
			b := *a
			b.ID, b.Priority = 2, 1
			b.Credentials = map[string]any{"api_key": "fixture-audit-B", "base_url": provider.URL}
			accounts = &readableWSAccounts{issue600SwitchRepo: &issue600SwitchRepo{&openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{*a, b}}}}
			*repo = accounts
		})
	finished := false
	defer func() {
		if !finished {
			issue600Finish(t, h, headers)
		}
	}()
	create := func(input string) string {
		return fmt.Sprintf(`{"type":"response.create","model":"gpt-5.1","client_metadata":{"thread_id":"fixture-audit-thread"},"tools":[{"type":"function","name":"inspect","parameters":{"type":"object","properties":{}}}],"input":%s}`, input)
	}
	initial := fmt.Sprintf(`[{"role":"user","content":%q},{"type":"function_call","name":"inspect","call_id":"call_audit_done","arguments":"{}"},{"type":"function_call_output","call_id":"call_audit_done","output":"completed-audit-result"}]`, recoveredWSAuditCanary)
	issue715WSSend(t, h.clientConn, create(initial))
	first := issue715WSRead(t, h.clientConn)
	require.Equal(t, "response.completed", gjson.GetBytes(first, "type").String())
	require.Equal(t, recoveredWSAuditAgent, gjson.GetBytes(first, "response.output.0").Raw)
	require.Eventually(t, func() bool {
		keys, err := probe.Keys(context.Background(), "openai_readable_shadow:v1:*").Result()
		if err != nil || len(keys) != 1 {
			return false
		}
		raw, err := probe.Get(context.Background(), keys[0]).Result()
		return err == nil && strings.Contains(raw, recoveredWSAuditCanary) && strings.Contains(raw, "completed-audit-result")
	}, 3*time.Second, 5*time.Millisecond, "A must really deliver and commit the exact readable task before disable")
	owners := issue805Owners(t, probe)
	require.Len(t, owners, 1)
	store.mu.Lock()
	store.records = nil
	store.mu.Unlock()
	prompt.armed.Store(true)
	accounts.disable(1)
	opaque := create("[" + recoveredWSAuditAgent + "]")
	require.NotContains(t, opaque, recoveredWSAuditCanary)
	issue715WSSend(t, h.clientConn, opaque)
	terminal := issue715WSRead(t, h.clientConn)
	if allowRecovery {
		require.Equal(t, "response.completed", gjson.GetBytes(terminal, "type").String(), string(terminal))
		require.Equal(t, int32(1), writesB.Load())
		issue715WSSend(t, h.clientConn, create(fmt.Sprintf(`[{"role":"user","content":%q}]`, recoveredWSAuditNext)))
		terminal = issue715WSRead(t, h.clientConn)
	} else {
		require.Zero(t, writesB.Load(), "WS_RECOVERED_AUDIT_DISPATCHED: restored unsafe task must be denied before any B generation; terminal=%s", terminal)
	}
	require.Equal(t, "error", gjson.GetBytes(terminal, "type").String(), string(terminal))
	wantCode := "content_policy_violation"
	wantClose := coderws.StatusPolicyViolation
	if policy == "prompt" {
		wantCode = "prompt_guard_blocked"
		wantClose = coderws.StatusCode(4403)
	}
	require.Equal(t, wantCode, gjson.GetBytes(terminal, "error.code").String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := h.clientConn.Read(ctx)
	require.Equal(t, wantClose, coderws.CloseStatus(err))
	select {
	case <-h.handlerDone:
	case <-ctx.Done():
		t.Fatal("audit denial did not terminate naturally")
	}
	finished = true
	select {
	case final := <-h.finalHeaders:
		for key, values := range headers {
			require.Equal(t, values, final.Values(key), "original client identity survives recovery")
		}
	case <-ctx.Done():
		t.Fatal("missing final identity")
	}
	require.Equal(t, int32(1), writesA.Load())
	expectedB := int32(0)
	if allowRecovery {
		expectedB = 1
	}
	require.Equal(t, expectedB, writesB.Load(), "denied turn must never be sent or retried")
	require.Positive(t, prompt.restored.Load(), "prompt audit must inspect restored content")
	require.True(t, prompt.private.Load())
	require.Equal(t, int64(4301), prompt.group.Load())
	store.mu.Lock()
	stored := append([]securityaudit.Event(nil), store.records...)
	store.mu.Unlock()
	require.NotEmpty(t, stored)
	for _, event := range stored {
		require.True(t, event.Snapshot.NoBodyRetention)
		require.NotNil(t, event.Snapshot.GroupID)
		require.EqualValues(t, 4301, *event.Snapshot.GroupID)
		require.Empty(t, event.Snapshot.ScanText)
		require.Equal(t, "[Recovered context withheld]", event.Snapshot.FullPrompt)
		require.Equal(t, "[Recovered context withheld]", event.Snapshot.RedactedPreview)
		require.Empty(t, event.ScannerEvidence)
	}
	if policy == "selected" {
		require.Equal(t, int32(1), selectedRestored.Load(), "selected B audit must inspect the restored task exactly once")
		require.Eventually(t, func() bool { return len(h.moderationRepo.logSnapshot()) >= 1 }, 3*time.Second, 5*time.Millisecond)
		for _, log := range h.moderationRepo.logSnapshot() {
			require.NotNil(t, log.GroupID)
			require.EqualValues(t, 4301, *log.GroupID)
			require.Equal(t, "[Recovered context withheld]", log.InputExcerpt)
			require.NotContains(t, log.Error, recoveredWSAuditCanary)
		}
	}
	require.Equal(t, owners, issue805Owners(t, probe), "A owner records must remain unchanged")
	if allowRecovery {
		bBodyMu.Lock()
		defer bBodyMu.Unlock()
		require.Len(t, bBodies, 1)
		require.Equal(t, 2, strings.Count(string(bBodies[0]), "call_audit_done"), "one completed call/result pair survives; no duplicate tool work")
		require.Equal(t, "fixture-audit-thread", gjson.GetBytes(bBodies[0], "client_metadata.thread_id").String())
	}
}
