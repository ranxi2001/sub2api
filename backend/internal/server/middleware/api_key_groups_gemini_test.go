package middleware

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupAllowsGooglePlatformBridgesNonGeminiModelsEndpoint(t *testing.T) {
	openai := &service.Group{Platform: service.PlatformOpenAI}
	gemini := &service.Group{Platform: service.PlatformGemini}
	typesafe := &service.Group{Platform: service.PlatformTypeSafe}

	require.True(t, groupAllowsGooglePlatform("/v1beta/models/gpt-4o:generateContent", openai))
	require.True(t, groupAllowsGooglePlatform("/v1beta/models", openai))
	require.False(t, groupAllowsGooglePlatform("/v1beta/cachedContents", openai))
	require.False(t, groupAllowsGooglePlatform("/antigravity/v1beta/models/gemini:generateContent", openai))
	require.True(t, groupAllowsGooglePlatform("/v1beta/cachedContents", gemini))
	require.False(t, groupAllowsGooglePlatform("/v1beta/models/jev:generateContent", typesafe))
}
