//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestGatewayIdentityDoesNotOptInExistingAccounts(t *testing.T) {
	var missing *Account
	require.False(t, missing.IsGateway())
	for _, kind := range []string{AccountTypeAPIKey, AccountTypeUpstream, AccountTypeOAuth, AccountTypeSetupToken, AccountTypeBedrock, AccountTypeServiceAccount, ""} {
		account := &Account{Type: kind, Extra: map[string]any{UpstreamBillingProviderExtraKey: "new_api"}}
		require.False(t, account.IsGateway(), kind)
	}
	require.True(t, (&Account{Type: AccountTypeGateway}).IsGateway())
}

func TestGatewayUsagePlatformRequiresConcreteRequestTarget(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeGateway}
	composite := &APIKey{Group: &Group{Platform: PlatformComposite}}
	for _, spec := range domain.Platforms() {
		t.Run(spec.ID, func(t *testing.T) {
			require.Equal(t, spec.ID, usagePlatformForRequest(context.Background(), account, composite, spec.ID), "async worker must use the handler snapshot")
			ctx := WithResolvedTargetPlatform(context.Background(), spec.ID)
			require.Equal(t, spec.ID, usagePlatformForRequest(ctx, account, composite, ""))
			require.Equal(t, spec.ID, usagePlatformForRequest(context.Background(), account, &APIKey{Group: &Group{Platform: spec.ID}}, ""))
		})
	}
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)
	require.Equal(t, PlatformAntigravity, usagePlatformForRequest(ctx, account, composite, ""))
	require.Equal(t, PlatformKimi, usagePlatformForRequest(ctx, account, composite, PlatformKimi), "captured target wins over the worker context")
	for _, invalid := range []string{"", PlatformComposite, "unknown"} {
		require.Empty(t, usagePlatformForRequest(context.Background(), account, composite, invalid))
	}
	require.Empty(t, usagePlatformForRequest(nil, account, nil, ""))
	require.Empty(t, usagePlatformForRequest(nil, nil, nil, ""))
	for _, kind := range []string{AccountTypeAPIKey, AccountTypeUpstream, AccountTypeOAuth} {
		account.Type = kind
		require.Equal(t, PlatformOpenAI, usagePlatformForRequest(ctx, account, composite, PlatformKimi), "old account attribution must remain unchanged")
	}
}

// This exercises both production usage writers, rather than only the platform
// helper. Composite requests arrive on an async worker with no routing context.
func TestGatewayUsageWritersSnapshotRequestTarget(t *testing.T) {
	for _, writer := range []string{"gateway", "openai"} {
		for _, spec := range domain.Platforms() {
			t.Run(writer+"/"+spec.ID, func(t *testing.T) {
				logs := &openAIRecordUsageLogRepoStub{inserted: true}
				users := &openAIRecordUsageUserRepoStub{}
				subs := &openAIRecordUsageSubRepoStub{}
				account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeGateway}
				key := &APIKey{ID: 1, Group: &Group{Platform: PlatformComposite, RateMultiplier: 1}}
				billing := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: false}}
				if writer == "openai" {
					svc := newOpenAIRecordUsageServiceForTest(logs, users, subs, nil)
					svc.usageBillingRepo = billing
					require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
						Result: &OpenAIForwardResult{RequestID: "gateway-platform-fixture", Model: "gpt-5.1", Usage: OpenAIUsage{InputTokens: 10}},
						APIKey: key, User: &User{ID: 2}, Account: account, QuotaPlatform: spec.ID,
					}))
				} else {
					svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, billing, users, subs)
					require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
						Result: &ForwardResult{RequestID: "gateway-platform-fixture", Model: "claude-sonnet-4", Usage: ClaudeUsage{InputTokens: 10}},
						APIKey: key, User: &User{ID: 2}, Account: account, QuotaPlatform: spec.ID,
					}))
				}
				require.NotNil(t, logs.lastLog)
				require.NotNil(t, billing.lastCmd)
				require.Equal(t, spec.ID, billing.lastCmd.PlatformQuotaPlatform)
				require.Equal(t, int64(3), logs.lastLog.AccountID, "one shared account identity")
				account.Platform = "changed-after-request"
				key.Group.Platform = "changed-after-request"
				require.Equal(t, spec.ID, logs.lastLog.Platform, "persist a value, not a reference to account/group state")
			})
		}
	}
}
