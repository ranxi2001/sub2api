package basispoints

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageToolRemainsRequestScopedWithCachedCatalog(t *testing.T) {
	cache := new(CatalogCache)
	source := fileDeliveryTestSource()
	generate := func(ImageGenerationRequest) (ImageGenerationResult, error) {
		t.Fatal("preparing a catalog must not generate an image")
		return ImageGenerationResult{}, nil
	}
	prepare := func(generator ImageGenerator) *Bridge {
		t.Helper()
		raw, err := json.Marshal(source)
		require.NoError(t, err)
		_, bridge, err := PrepareWithCatalogAndImageGeneration(raw, "account/key/thread", nil, cache, generator)
		require.NoError(t, err)
		return bridge
	}
	first := prepare(generate)
	require.NotNil(t, first.imageGenerator)
	require.True(t, first.imageFileDelivery)
	snapshot, _ := cache.snapshot("account/key/thread")
	require.NotContains(t, string(snapshot), ImageGenerationToolName)
	delete(source, "tools")
	inherited := prepare(generate)
	require.NotNil(t, inherited.imageGenerator)
	require.True(t, inherited.imageFileDelivery)
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	_, uploaded, err := inherited.Reprepare(raw)
	require.NoError(t, err)
	require.NotNil(t, uploaded.imageGenerator)
	require.True(t, uploaded.imageFileDelivery)
	// Disabling the account bridge must not inherit a server-only tool.
	disabled := prepare(nil)
	require.Nil(t, disabled.imageGenerator)
	require.NotContains(t, disabled.tools, ImageGenerationToolName)
	require.NotEmpty(t, disabled.tools)
}

func TestCachedClientImageToolPreventsServerInjection(t *testing.T) {
	cache := new(CatalogCache)
	source := testSource()
	source["tools"] = []any{object{"type": "custom", "name": "image_gen.imagegen"}}
	generate := func(ImageGenerationRequest) (ImageGenerationResult, error) {
		t.Fatal("the client owns image generation")
		return ImageGenerationResult{}, nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := json.Marshal(source)
		require.NoError(t, err)
		_, bridge, err := PrepareWithCatalogAndImageGeneration(raw, "account/key/thread", nil, cache, generate)
		require.NoError(t, err)
		require.Nil(t, bridge.imageGenerator)
		require.Contains(t, bridge.tools, "image_gen.imagegen")
		delete(source, "tools")
	}
}
