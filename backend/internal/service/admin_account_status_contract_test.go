package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOrdinaryAccountEditPreservesConcurrentAuthenticationState(t *testing.T) {
	for _, billing := range []bool{false, true} {
		for _, explicitStatus := range []string{"", StatusDisabled} {
			name := "fallback/ordinary"
			if billing {
				name = "billing/ordinary"
			}
			if explicitStatus != "" {
				name += "/explicit-status"
			}
			t.Run(name, func(t *testing.T) {
				r := &accountTimerEditContractRepo{loaded: Account{
					ID: 7, Name: "before", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
					Status: StatusActive, Schedulable: true, Extra: map[string]any{},
				}}
				r.current = r.loaded
				r.current.Status = StatusError
				r.current.ErrorMessage = "401 authentication failed"
				var repo AccountRepository = r
				if billing {
					repo = &accountTimerBillingEditContractRepo{r}
				}
				svc := &adminServiceImpl{accountRepo: repo}
				// A caller's inherited marker cannot suppress an explicit status edit.
				ctx := WithPreservedAccountStatus(context.Background())
				_, err := svc.UpdateAccount(ctx, 7, &UpdateAccountInput{Name: "after", Status: explicitStatus})
				require.NoError(t, err)
				require.NotNil(t, r.updateContext)
				require.Equal(t, explicitStatus == "", PreserveAccountStatus(r.updateContext))
			})
		}
	}
}

func TestAccountServiceEditRetainsStatusOwnership(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "explicit-status"}[explicit], func(t *testing.T) {
			r := &accountTimerEditContractRepo{loaded: Account{
				ID: 7, Name: "before", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Status: StatusActive, Schedulable: true, Extra: map[string]any{},
			}}
			r.current = r.loaded
			name, status := "after", StatusDisabled
			input := UpdateAccountRequest{Name: &name}
			if explicit {
				input.Status = &status
			}
			_, err := NewAccountService(r, nil).Update(WithPreservedAccountStatus(context.Background()), 7, input)
			require.NoError(t, err)
			require.Equal(t, !explicit, PreserveAccountStatus(r.updateContext))
		})
	}
}
