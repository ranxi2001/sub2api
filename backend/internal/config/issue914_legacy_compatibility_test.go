package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIssue914LegacyCompatibilityConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, env string
		want            bool
	}{
		{"default", "", "", false},
		{"yaml", "gateway:\n  openai_legacy_compatibility_mode: true\n", "", true},
		{"env", "", "true", true},
		{"env_over_yaml", "gateway:\n  openai_legacy_compatibility_mode: true\n", "false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte(tc.yaml), 0600))
			t.Setenv("CONFIG_FILE", path)
			t.Setenv("GATEWAY_OPENAI_LEGACY_COMPATIBILITY_MODE", tc.env)
			cfg, err := Load()
			require.NoError(t, err)
			raw, err := json.Marshal(cfg.Gateway)
			require.NoError(t, err)
			var got struct{ OpenAILegacyCompatibilityMode bool }
			require.NoError(t, json.Unmarshal(raw, &got))
			require.Equal(t, tc.want, got.OpenAILegacyCompatibilityMode)
		})
	}
}
