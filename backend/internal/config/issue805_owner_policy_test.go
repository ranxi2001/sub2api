package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIssue805OwnerPolicyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, env string
		want            bool
	}{
		{"default", "", "", false},
		{"yaml_false", "gateway:\n  disable_encrypted_agent_owner_binding: false\n", "", false},
		{"yaml_true", "gateway:\n  disable_encrypted_agent_owner_binding: true\n", "", true},
		{"env_true", "", "true", true},
		{"env_false_over_yaml", "gateway:\n  disable_encrypted_agent_owner_binding: true\n", "false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte(tc.yaml), 0600))
			t.Setenv("CONFIG_FILE", path)
			t.Setenv("GATEWAY_DISABLE_ENCRYPTED_AGENT_OWNER_BINDING", tc.env)
			cfg, err := Load()
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.Gateway.DisableEncryptedAgentOwnerBinding)
		})
	}
}
