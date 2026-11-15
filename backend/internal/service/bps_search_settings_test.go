package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBPSSearchDefaultsAndQualityPolicyRoundTrip(t *testing.T) {
	defaults := DefaultExcelBPSDefaults()
	require.False(t, defaults.WebSearch)
	require.False(t, defaults.WebSearchAuto)
	for _, enabled := range []bool{false, true} {
		defaults.WebSearch, defaults.WebSearchAuto = enabled, true
		raw, err := json.Marshal(defaults)
		require.NoError(t, err)
		var restored ExcelBPSDefaults
		require.NoError(t, json.Unmarshal(raw, &restored))
		require.Equal(t, enabled, restored.extra()[ExcelBPSWebSearchKey])
		require.Equal(t, enabled, restored.extra()[ExcelBPSWebSearchAutoKey])
		extra := QualityBPSExtra(&QualityBPSPolicy{WebSearch: enabled, WebSearchAuto: true})
		require.Equal(t, enabled, extra[ExcelBPSWebSearchKey])
		require.Equal(t, enabled, extra[ExcelBPSWebSearchAutoKey])
	}
	require.Contains(t, QualityBPSManagedKeys, ExcelBPSWebSearchKey)
	require.Contains(t, QualityBPSManagedKeys, ExcelBPSWebSearchAutoKey)
	var legacy ExcelBPSDefaults
	require.NoError(t, json.Unmarshal([]byte(`{"models":["gpt-6-astra"]}`), &legacy))
	require.False(t, legacy.WebSearch)
	require.False(t, legacy.WebSearchAuto)
}
