package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestStrictSameAccountRetryRequiresCurrentBinding(t *testing.T) {
	for _, owner := range []int64{947101, 947102, 0} {
		t.Run(fmt.Sprint(owner), func(t *testing.T) {
			f := newStrictSessionFixture()
			f.cache.sessionBindings["openai:rebind-session"] = owner
			ctx := WithOpenAISameAccountRetry(context.Background(), 947101, &UpstreamFailoverError{RetryableOnSameAccount: true})
			selected, _, err := f.scheduler.Select(ctx, f.request)
			if owner == 947101 {
				require.NoError(t, err)
				require.Equal(t, owner, selected.Account.ID)
			} else {
				require.Error(t, err)
				require.Nil(t, selected)
			}
			require.Equal(t, owner, f.cache.sessionBindings["openai:rebind-session"])
		})
	}
}

func TestSameAccountRetrySchedulerCannotEscape(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		for _, weighted := range []string{"false", "true"} {
			for _, scenario := range []string{"busy", "recovered", "excluded", "disabled", "wrong_group", "no_session", "expired", "canceled"} {
				t.Run(fmt.Sprintf("advanced=%s/weighted=%s/%s", advanced, weighted, scenario), func(t *testing.T) {
					groupID := int64(10103)
					accounts := []Account{
						{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}},
						{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}},
					}
					cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:retry-session": 1}}
					cfg := &config.Config{}
					cfg.Gateway.OpenAIScheduler.StickyEscapeEnabled = true
					cfg.Gateway.Scheduling.StickySessionWaitTimeout = time.Second
					var acquired []int64
					acquires := map[int64]bool{1: false, 2: true}
					var excluded map[int64]struct{}
					session := "retry-session"
					failure := &UpstreamFailoverError{RetryableOnSameAccount: true, SameAccountRetryDeadline: time.Now().Add(time.Second)}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					switch scenario {
					case "recovered":
						acquires[1] = true
					case "excluded":
						excluded = map[int64]struct{}{1: {}}
					case "disabled":
						accounts[0].Schedulable = false
					case "wrong_group":
						accounts[0].GroupIDs = []int64{999}
					case "no_session":
						session = ""
					case "expired":
						failure.SameAccountRetryDeadline = time.Now().Add(-time.Second)
					case "canceled":
						cancel()
					}
					svc := &OpenAIGatewayService{
						accountRepo:      schedulerTestOpenAIAccountRepo{accounts: accounts},
						cache:            cache,
						cfg:              cfg,
						rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(advanced, weighted),
						concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{
							acquireResults: acquires, acquiredIDs: &acquired,
						}),
					}
					ctx = WithOpenAISameAccountRetry(ctx, 1, failure)
					selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", session, "gpt-5.1", excluded, OpenAIUpstreamTransportAny, false)
					if scenario == "busy" || scenario == "no_session" || scenario == "recovered" {
						require.NoError(t, err)
						require.Equal(t, int64(1), selection.Account.ID)
						if scenario == "recovered" {
							require.True(t, selection.Acquired)
							selection.ReleaseFunc()
						} else {
							require.NotNil(t, selection.WaitPlan)
							require.Positive(t, selection.WaitPlan.Timeout)
							require.LessOrEqual(t, selection.WaitPlan.Timeout, time.Second)
						}
					} else {
						require.Error(t, err)
						require.Nil(t, selection)
					}
					require.NotContains(t, acquired, int64(2), "B is healthy but cannot replace the retry owner")
					require.Equal(t, int64(1), cache.sessionBindings["openai:retry-session"])
				})
			}
		}
	}
}
