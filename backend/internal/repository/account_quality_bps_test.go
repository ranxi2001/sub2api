package repository

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQualityBPSSnapshotEqualDefaultsAndManualChanges(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after map[string]any
		equal         bool
	}{
		{"omitted false switches", map[string]any{"openai_excel_bps": true, "openai_excel_bps_cache_creation_as_input": false, "openai_excel_bps_mihomo": false}, map[string]any{"openai_excel_bps": true}, true},
		{"omitted search switches", map[string]any{"openai_excel_bps_web_search": false, "openai_excel_bps_web_search_auto": false}, nil, true},
		{"manual search disable", map[string]any{"openai_excel_bps_web_search": true}, nil, false},
		{"manual automatic search disable", map[string]any{"openai_excel_bps_web_search_auto": true}, nil, false},
		{"default recovery interval", map[string]any{"openai_excel_bps_403_recovery_interval_minutes": 60}, nil, true},
		{"custom recovery interval", map[string]any{"openai_excel_bps_403_recovery_interval_minutes": 30}, nil, false},
		{"default proxy", map[string]any{"openai_excel_bps_proxy_source": "mihomo"}, nil, true},
		{"manual disable", map[string]any{"openai_excel_bps": true}, nil, false},
		{"manual toggle", map[string]any{"openai_excel_bps_cache_creation_as_input": true}, nil, false},
		{"manual proxy change", map[string]any{"openai_excel_bps_proxy_source": "ip_pool"}, nil, false},
		{"model scope removed", map[string]any{"openai_excel_bps_models": []string{"gpt-6-astra"}}, nil, false},
		{"empty models are not all models", map[string]any{"openai_excel_bps_models": []string{}}, nil, false},
		{"null models are not all models", map[string]any{"openai_excel_bps_models": nil}, nil, false},
		{"target group removed", map[string]any{"openai_excel_bps_403_target_group_id": 0}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			toRaw := func(value map[string]any) map[string]json.RawMessage {
				data, err := json.Marshal(value)
				require.NoError(t, err)
				var raw map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(data, &raw))
				return raw
			}
			before, after := toRaw(tc.before), toRaw(tc.after)
			require.Equal(t, tc.equal, qualityBPSSnapshotEqual(before, after))
			require.Equal(t, tc.equal, qualityBPSSnapshotEqual(after, before))
		})
	}
}
