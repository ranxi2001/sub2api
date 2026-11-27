//go:build unit

package admin

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIssue1081SettingRoundTripAndLegacyOmission(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{service.SettingKeySiteName: "Fixture"})
	for _, enabled := range []bool{false, true, false} {
		rec := doUpdateSettings(t, h, map[string]any{"api_key_multi_group_enabled": enabled}, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		want := "false"
		if enabled {
			want = "true"
		}
		require.Equal(t, want, repo.values[service.SettingKeyAPIKeyMultiGroupEnabled])
		rec = doUpdateSettings(t, h, map[string]any{"site_name": "Fixture"}, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, want, repo.values[service.SettingKeyAPIKeyMultiGroupEnabled])
	}
}
