package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestPluginSSEEarlyEventsConfiguration(t *testing.T) {
	t.Run("default disabled", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		t.Setenv("GATEWAY_OPENAI_PLUGIN_SSE_EARLY_EVENTS_ENABLED", "")
		setDefaults()
		require.False(t, viper.GetBool("gateway.openai_plugin_sse_early_events_enabled"))
		cfg, err := Load()
		require.NoError(t, err)
		require.False(t, cfg.Gateway.OpenAIPluginSSEEarlyEventsEnabled)
	})
	t.Run("explicit environment opt in", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		t.Setenv("GATEWAY_OPENAI_PLUGIN_SSE_EARLY_EVENTS_ENABLED", "true")
		cfg, err := Load()
		require.NoError(t, err)
		require.True(t, cfg.Gateway.OpenAIPluginSSEEarlyEventsEnabled)
	})
}
