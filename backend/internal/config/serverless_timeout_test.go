package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadServerlessResponseHeaderTimeoutDefault(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("RUNTIME_SERVERLESS_RESPONSE_HEADER_TIMEOUT", "")

	cfg, err := Load()
	require.NoError(t, err)
	require.Zero(t, cfg.Runtime.ServerlessResponseHeaderTimeout)
}

func TestLoadServerlessResponseHeaderTimeoutFromEnvironment(t *testing.T) {
	for _, seconds := range []int{0, 600, 86400} {
		t.Run(strconv.Itoa(seconds), func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv("RUNTIME_SERVERLESS_RESPONSE_HEADER_TIMEOUT", strconv.Itoa(seconds))

			cfg, err := Load()
			require.NoError(t, err)
			require.Equal(t, seconds, cfg.Runtime.ServerlessResponseHeaderTimeout)
		})
	}
}

func TestLoadServerlessResponseHeaderTimeoutFromConfig(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("RUNTIME_SERVERLESS_RESPONSE_HEADER_TIMEOUT", "")
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte("runtime:\n  serverless_response_header_timeout: 900\n"), 0o600))
	t.Setenv("CONFIG_FILE", configFile)

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 900, cfg.Runtime.ServerlessResponseHeaderTimeout)

	// An explicit zero must be able to disable a configured finite budget.
	t.Setenv("RUNTIME_SERVERLESS_RESPONSE_HEADER_TIMEOUT", "0")
	cfg, err = Load()
	require.NoError(t, err)
	require.Zero(t, cfg.Runtime.ServerlessResponseHeaderTimeout)
}

func TestServerlessResponseHeaderTimeoutValidation(t *testing.T) {
	for _, seconds := range []int{0, 1, 600, 86400} {
		cfg := &Config{Runtime: RuntimeConfig{ServerlessResponseHeaderTimeout: seconds}}
		require.NoError(t, cfg.validateRuntime(), "seconds=%d", seconds)
	}
	for _, seconds := range []int{-1, 86401, int(^uint(0) >> 1)} {
		cfg := &Config{Runtime: RuntimeConfig{ServerlessResponseHeaderTimeout: seconds}}
		require.ErrorContains(t, cfg.validateRuntime(), "runtime.serverless_response_header_timeout", "seconds=%d", seconds)
	}
}

func TestLoadServerlessResponseHeaderTimeoutRejectsInvalidEnvironment(t *testing.T) {
	for _, value := range []string{"-1", "86401"} {
		t.Run(value, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv("RUNTIME_SERVERLESS_RESPONSE_HEADER_TIMEOUT", value)

			_, err := Load()
			require.ErrorContains(t, err, "runtime.serverless_response_header_timeout")
		})
	}
}
