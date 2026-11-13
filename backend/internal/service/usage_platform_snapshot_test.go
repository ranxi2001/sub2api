//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestUsagePlatformSnapshotsSelectedAccount(t *testing.T) {
	for _, spec := range domain.Platforms() {
		t.Run(spec.ID, func(t *testing.T) {
			logs := &openAIRecordUsageLogRepoStub{inserted: true}
			users := &openAIRecordUsageUserRepoStub{}
			subs := &openAIRecordUsageSubRepoStub{}
			account := &Account{ID: 3, Platform: spec.ID, Type: AccountTypeAPIKey}
			key := &APIKey{ID: 1, Group: &Group{Platform: PlatformComposite, RateMultiplier: 1}}
			if spec.Gateway == domain.PlatformGatewayOpenAI {
				svc := newOpenAIRecordUsageServiceForTest(logs, users, subs, nil)
				svc.cfg.RunMode = config.RunModeSimple
				require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
					Result: &OpenAIForwardResult{RequestID: "platform-fixture", Model: "gpt-5.1", Usage: OpenAIUsage{InputTokens: 10}},
					APIKey: key, User: &User{ID: 2}, Account: account,
				}))
			} else {
				svc := newGatewayRecordUsageServiceForTest(logs, users, subs)
				svc.cfg.RunMode = config.RunModeSimple
				require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
					Result: &ForwardResult{RequestID: "platform-fixture", Model: "claude-sonnet-4", Usage: ClaudeUsage{InputTokens: 10}},
					APIKey: key, User: &User{ID: 2}, Account: account,
				}))
			}
			require.NotNil(t, logs.lastLog)
			account.Platform = "changed-after-request"
			key.Group.Platform = "changed-after-request"
			raw, err := json.Marshal(logs.lastLog)
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(raw, &fields))
			require.Equal(t, spec.ID, fields["Platform"], "usage must snapshot the selected account platform")
		})
	}
}
