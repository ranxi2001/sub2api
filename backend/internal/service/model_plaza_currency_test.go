package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelPlazaCurrencyRuntime(t *testing.T) {
	for _, tc := range []struct {
		name, fx, credits   string
		wantFX, wantCredits float64
	}{
		{"legacy defaults", "", "", 7, 1},
		{"separate conversion rates", "7.2", "2", 7.2, 2},
		{"invalid stored values", "NaN", "-1", 7, 1},
		{"nonfinite stored values", "+Inf", "+Inf", 7, 1},
		{"zero", "0", "0", 7, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &showcaseRuntimeSettingRepo{values: map[string]string{
				SettingKeyModelPlazaEnabled: "true", SettingKeyModelPlazaUSDToCNYRate: tc.fx, SettingBalanceRechargeMult: tc.credits,
			}}
			svc := &SettingService{settingRepo: repo}
			got := svc.GetModelPlazaRuntime(context.Background())
			require.True(t, got.Enabled)
			require.Equal(t, tc.wantFX, got.USDToCNYRate)
			require.Equal(t, tc.wantCredits, got.CreditPerCNY)
			require.Nil(t, repo.saved, "display lookup must not rewrite payment or pricing settings")
		})
	}
	svc := &SettingService{settingRepo: &showcaseRuntimeSettingRepo{readErr: errors.New("unavailable")}}
	require.False(t, svc.GetModelPlazaRuntime(context.Background()).Enabled)
}
