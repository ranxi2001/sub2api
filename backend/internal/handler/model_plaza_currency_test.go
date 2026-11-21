package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestModelPlazaCurrencyRulesRespectVisibleModels(t *testing.T) {
	settings, err := service.NormalizeModelPlazaCurrency(service.ModelPlazaCurrencySettings{
		OfficialPlatformCurrencies: map[string]string{"visible-platform": "CNY", "hidden-platform": "USD"},
		OfficialModelCurrencies:    map[string]string{"visible-model": "CNY", "hidden-model": "USD"},
	})
	require.NoError(t, err)
	out := visiblePlazaCurrency(settings, []modelPlazaGroup{{Models: []modelPlazaModel{{Name: "VISIBLE-MODEL", Platform: "visible-platform"}}}})
	require.Equal(t, map[string]string{"visible-platform": "CNY"}, out.OfficialPlatformCurrencies)
	require.Equal(t, map[string]string{"visible-model": "CNY"}, out.OfficialModelCurrencies)
	require.Len(t, settings.OfficialModelCurrencies, 2, "filtering must not mutate stored rules")
	require.Empty(t, visiblePlazaCurrency(settings, nil).OfficialModelCurrencies)
}
