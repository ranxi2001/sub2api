//go:build unit

package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"math"
	"strconv"
	"testing"
)

func TestModelPlazaDisplayRateDoesNotWritePaymentSettings(t *testing.T) {
	repo := &settingUpdateRepoStub{}
	svc := NewSettingService(repo, &config.Config{})
	for _, rate := range []float64{7, 7.2} {
		updates, err := svc.buildSystemSettingsUpdates(context.Background(), &SystemSettings{ModelPlazaUSDToCNYRate: rate})
		require.NoError(t, err)
		require.Equal(t, strconv.FormatFloat(rate, 'f', -1, 64), updates[SettingKeyModelPlazaUSDToCNYRate])
		require.NotContains(t, updates, SettingBalanceRechargeMult)
		require.NotContains(t, updates, SettingSubscriptionUSDToCNYRate)
	}
	for _, rate := range []float64{-1, math.NaN(), math.Inf(1)} {
		_, err := svc.buildSystemSettingsUpdates(context.Background(), &SystemSettings{ModelPlazaUSDToCNYRate: rate})
		require.ErrorContains(t, err, "display rate")
	}
}
