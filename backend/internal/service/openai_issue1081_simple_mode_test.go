package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func issue1081OpenAIContext(t *testing.T, groups []int64, disabled bool) (context.Context, *APIKey) {
	t.Helper()
	primary := int64(7)
	key := &APIKey{GroupID: &primary, GroupIDs: groups, Group: &Group{ID: primary, Platform: PlatformOpenAI, Status: StatusActive}}
	if disabled {
		var err error
		key, err = primaryGroupOnly(key)
		require.NoError(t, err)
	}
	return WithAPIKeyGroupConstraint(context.Background(), key), key
}

func TestIssue1081OpenAISimpleModeRouting(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		groups                    []int64
		disabled, primaryEligible bool
		wantID                    int64
	}{
		{name: "extra_only", groups: []int64{7, 8}, disabled: true},
		{name: "main_beats_better_extra", groups: []int64{7, 8}, disabled: true, primaryEligible: true, wantID: 71},
		{name: "enabled_legacy", groups: []int64{7, 8}, wantID: 81},
		{name: "single_legacy", groups: []int64{7}, disabled: true, wantID: 81},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, key := issue1081OpenAIContext(t, tc.groups, tc.disabled)
			extra := Account{ID: 81, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{8}, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.1": "gpt-5.1"}}}
			accounts := []Account{extra}
			if tc.primaryEligible {
				main := extra
				main.ID, main.GroupIDs, main.Priority = 71, []int64{7}, 100
				accounts = append(accounts, main)
			}
			repo := schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}
			svc := &OpenAIGatewayService{accountRepo: repo, cfg: &config.Config{RunMode: config.RunModeSimple}}
			selected, err := svc.SelectAccountForModelWithExclusions(ctx, key.GroupID, "", "gpt-5.1", nil)
			if tc.wantID == 0 {
				require.Error(t, err)
				require.Nil(t, selected, "ISSUE1081_OPENAI_EXTRA_ACCOUNT_SELECTED")
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.wantID, selected.ID)
			}
		})
	}
}

func TestIssue1081OpenAISimpleModeFirstWrite(t *testing.T) {
	for _, transport := range []string{"http", "ws"} {
		t.Run(transport, func(t *testing.T) {
			ctx, key := issue1081OpenAIContext(t, []int64{7, 8}, true)
			account := groupAdmissionAccount(8)
			account.Credentials["model_mapping"] = map[string]any{"gpt-5.1": "gpt-5.1"}
			conn := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_1081","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)}}
			svc, dialer := newGroupAdmissionWSv2Service(t, &groupAdmissionRepo{first: account}, conn)
			svc.cfg.RunMode = config.RunModeSimple
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"resp_1081","model":"gpt-5.1","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))}}
			svc.httpUpstream = upstream
			if transport == "http" {
				account.Type = AccountTypeAPIKey
				account.Extra = nil
				account.Credentials["api_key"] = "fixture-key"
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			c.Set("api_key", key)
			result, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-5.1","stream":false,"input":"hello"}`))
			require.Nil(t, result)
			requireGroupMembershipDenied(t, err)
			require.Zero(t, dialer.DialCount(), "ISSUE1081_EXTRA_ACCOUNT_DIALED")
			require.Nil(t, conn.lastWrite, "ISSUE1081_EXTRA_ACCOUNT_WS_WRITE")
			require.Nil(t, upstream.lastReq, "ISSUE1081_EXTRA_ACCOUNT_HTTP_WRITE")
		})
	}
}

func TestIssue1081OpenAISimpleModePreviousOwnerCannotMove(t *testing.T) {
	ctx, key := issue1081OpenAIContext(t, []int64{7, 8}, true)
	extra := Account{ID: 81, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{8}, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.1": "gpt-5.1"}}}
	main := extra
	main.ID, main.GroupIDs = 71, []int64{7}
	repo := schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{extra, main}}}
	cache := &schedulerTestGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	require.NoError(t, store.BindResponseAccount(ctx, 7, "resp_1081_extra_owner", extra.ID, time.Hour))
	acquired := []int64{}
	svc := &OpenAIGatewayService{accountRepo: repo, cache: cache, cfg: &config.Config{RunMode: config.RunModeSimple}, openaiWSStateStore: store, concurrencyService: NewConcurrencyService(&schedulerTestConcurrencyCache{acquiredIDs: &acquired})}
	selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, key.GroupID, "resp_1081_extra_owner", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses, false, true, true)
	require.Nil(t, selection)
	requireGroupMembershipDenied(t, err)
	require.Empty(t, acquired, "ISSUE1081_CONTINUATION_REPLAYED_ON_ANOTHER_ACCOUNT")
	owner, err := store.GetResponseAccount(ctx, 7, "resp_1081_extra_owner")
	require.NoError(t, err)
	require.Equal(t, extra.ID, owner)
}

// The diagnosis authority is deliberately independent of scheduling queries.
// Return all rows here so the request predicate must reject an extra-group row
// even if a stale/custom reader overstates its pool.
type issue1081OpenAIAvailabilityRepo struct {
	schedulerGroupAwareOpenAIAccountRepo
}

func (r issue1081OpenAIAvailabilityRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func TestIssue1081OpenAISimpleModeDiagnosis(t *testing.T) {
	ctx, key := issue1081OpenAIContext(t, []int64{7, 8}, true)
	extra := Account{ID: 81, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{8}, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.1": "gpt-5.1"}}}
	repo := issue1081OpenAIAvailabilityRepo{schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{extra}}}}
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: &config.Config{RunMode: config.RunModeSimple}}
	diagnosis := svc.DiagnoseModelAvailabilityForPlatform(ctx, key.GroupID, "gpt-5.1", PlatformOpenAI)
	require.True(t, diagnosis.Complete)
	require.False(t, diagnosis.HasAccountsInPool)
	require.False(t, diagnosis.HasModelSupport, "ISSUE1081_EXTRA_ACCOUNT_DIAGNOSED")
	require.Zero(t, diagnosis.ConfiguredAccounts)
}

func TestIssue1081OpenAISimpleModeLatestAdmission(t *testing.T) {
	ctx, key := issue1081OpenAIContext(t, []int64{7, 8}, true)
	selected := groupAdmissionAccount(7)
	latest := *selected
	latest.GroupIDs = []int64{8}
	repo := &turnAdmissionRepo{account: &latest}
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: &config.Config{RunMode: config.RunModeSimple}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	c.Set("api_key", key)
	got, err := svc.admitOpenAITurn(ctx, c, selected, "gpt-5.1")
	require.Nil(t, got)
	requireGroupMembershipDenied(t, err)
	require.Equal(t, 1, repo.reads)
}

func TestIssue1081OpenAISimpleModeStickyAndAcquisition(t *testing.T) {
	ctx, key := issue1081OpenAIContext(t, []int64{7, 8}, true)
	extra := Account{ID: 81, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{8}, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.1": "gpt-5.1"}}}
	main := extra
	main.ID, main.GroupIDs, main.Priority = 71, []int64{7}, 100
	for _, includeMain := range []bool{false, true} {
		t.Run(map[bool]string{false: "extra_only", true: "main_eligible"}[includeMain], func(t *testing.T) {
			accounts := []Account{extra}
			if includeMain {
				accounts = append(accounts, main)
			}
			cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"sticky-extra": extra.ID}}
			acquired := []int64{}
			repo := schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}
			svc := &OpenAIGatewayService{accountRepo: repo, cache: cache, cfg: &config.Config{RunMode: config.RunModeSimple}, concurrencyService: NewConcurrencyService(&schedulerTestConcurrencyCache{acquiredIDs: &acquired})}
			selection, err := svc.SelectAccountWithLoadAwareness(ctx, key.GroupID, "sticky-extra", "gpt-5.1", nil)
			if includeMain {
				require.NoError(t, err)
				require.Equal(t, main.ID, selection.Account.ID)
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
			} else {
				require.Error(t, err)
				require.Nil(t, selection)
			}
			require.NotContains(t, acquired, extra.ID, "ISSUE1081_EXTRA_ACCOUNT_ACQUIRED")
		})
	}
}

func TestIssue1081OpenAISimpleModeContinuationStaysOnPrimaryOwner(t *testing.T) {
	for _, excluded := range []bool{false, true} {
		t.Run(map[bool]string{false: "primary_owner_eligible", true: "primary_owner_excluded"}[excluded], func(t *testing.T) {
			ctx, key := issue1081OpenAIContext(t, []int64{7, 8}, true)
			owner := Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{7}, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.1": "gpt-5.1"}}, Extra: map[string]any{"openai_apikey_responses_websockets_v2_enabled": true}}
			other := owner
			other.ID = 72
			repo := schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{owner, other}}}
			cache := &schedulerTestGatewayCache{}
			store := NewOpenAIWSStateStore(cache)
			require.NoError(t, store.BindResponseAccount(ctx, 7, "resp_primary_1081", owner.ID, time.Hour))
			acquired := []int64{}
			svc := &OpenAIGatewayService{accountRepo: repo, cache: cache, cfg: &config.Config{RunMode: config.RunModeSimple}, openaiWSStateStore: store, concurrencyService: NewConcurrencyService(&schedulerTestConcurrencyCache{acquiredIDs: &acquired})}
			exclusions := map[int64]struct{}{}
			if excluded {
				exclusions[owner.ID] = struct{}{}
			}
			selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, key.GroupID, "resp_primary_1081", "", "gpt-5.1", exclusions, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses, false, true, true)
			if excluded {
				require.Nil(t, selection)
				require.Error(t, err)
				require.Empty(t, acquired, "continuation must not move to another primary account")
			} else {
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.Equal(t, owner.ID, selection.Account.ID)
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
			}
			bound, err := store.GetResponseAccount(ctx, 7, "resp_primary_1081")
			require.NoError(t, err)
			require.Equal(t, owner.ID, bound)
		})
	}
}
