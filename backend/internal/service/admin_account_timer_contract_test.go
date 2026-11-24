package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/accounttimer"
	"github.com/stretchr/testify/require"
)

// A timer changes the stored bit after the editor's initial read. Both repository
// update capabilities must receive an ordinary-edit context, never a manual switch.
type accountTimerEditContractRepo struct {
	AccountRepository
	loaded        Account
	current       Account
	reads         int
	updateContext context.Context
}

func (r *accountTimerEditContractRepo) GetByID(context.Context, int64) (*Account, error) {
	r.reads++
	account := r.current
	if r.reads == 1 {
		account = r.loaded
	}
	return &account, nil
}
func (r *accountTimerEditContractRepo) Update(ctx context.Context, a *Account) error {
	r.updateContext = ctx
	r.current.Name = a.Name
	return nil
}

type accountTimerBillingEditContractRepo struct{ *accountTimerEditContractRepo }

func (r *accountTimerBillingEditContractRepo) UpdateWithAccountBillingSettings(ctx context.Context, a *Account, _ AccountBillingSettingsUpdate) error {
	return r.Update(ctx, a)
}

func TestAccountTimerOrdinaryEditPreservesConcurrentScheduling(t *testing.T) {
	for _, billing := range []bool{false, true} {
		for _, loaded := range []bool{false, true} {
			name := "fallback"
			if billing {
				name = "billing"
			}
			if loaded {
				name += "/timer-closed"
			} else {
				name += "/timer-opened"
			}
			t.Run(name, func(t *testing.T) {
				r := &accountTimerEditContractRepo{loaded: Account{ID: 7, Name: "before", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: loaded, Extra: map[string]any{}}}
				r.current = r.loaded
				r.current.Schedulable = !loaded
				var repo AccountRepository = r
				if billing {
					repo = &accountTimerBillingEditContractRepo{r}
				}
				svc := &adminServiceImpl{accountRepo: repo}
				// Even an inherited switch context cannot turn this DTO into a manual intent.
				updated, err := svc.UpdateAccount(accounttimer.WithManualIntent(context.Background(), 19), 7, &UpdateAccountInput{Name: "after"})
				require.NoError(t, err)
				require.Equal(t, "after", updated.Name)
				require.Equal(t, !loaded, updated.Schedulable)
				require.NotNil(t, r.updateContext)
				require.True(t, accounttimer.PreserveSchedulable(r.updateContext))
				require.Zero(t, accounttimer.ManualActor(r.updateContext), "ordinary edits must not cancel pending plans")
			})
		}
	}
}
