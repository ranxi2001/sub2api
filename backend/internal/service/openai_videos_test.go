//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIVideoRequestValidation(t *testing.T) {
	for _, body := range []string{
		`{`,
		"[]",
		`{"model":1,"seconds":5}`,
		`{"model":"example","seconds":0}`,
		`{"model":"example","seconds":1.2}`,
		`{"model":"example","seconds":-2}`,
		`{"model":"example","seconds":5,"duration":6}`,
		`{"model":"example","seconds":5,"n":2}`,
		`{"model":"example","seconds":5,"seconds":30}`,
		`{"model":"example","model":"other","seconds":5}`,
		`{"model":"example","Model":"other","seconds":5}`,
	} {
		_, err := ParseOpenAIVideoRequest("application/json", []byte(body))
		require.Error(t, err, body)
	}
	info, err := ParseOpenAIVideoRequest("application/json", []byte(`{"model":"example","prompt":"waves","duration":"30","size":"720x1280","metadata":{"content":[{"image_url":"https://example.com/ref.png"}]}}`))
	require.NoError(t, err)
	require.Equal(t, 30, info.DurationSeconds)
	require.Equal(t, "720p", info.Resolution)
	require.Equal(t, []string{"https://example.com/ref.png"}, info.InputImageURLs)
}

func TestOpenAIVideoAccountOptIn(t *testing.T) {
	account := seedanceTestAccount()
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityVideos))
	require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySeedance))
	account.Credentials["openai_capabilities"] = []string{"seedance", "videos"}
	require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityVideos))
	require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySeedance))
	account.Type = AccountTypeOAuth
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityVideos))
}

func TestOpenAIVideoChannelPrice(t *testing.T) {
	for _, tc := range []struct {
		mode BillingMode
		want float64
	}{
		{BillingModeVideo, 12},
		{BillingModePerRequest, 0.4},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			groupID := int64(32)
			price := 0.4
			svc := newOpenAIRecordUsageServiceForTest(nil, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			cache := newEmptyChannelCache()
			cache.channelByGroupID[groupID] = &Channel{ID: groupID, Status: StatusActive}
			cache.pricingByGroupModel[channelModelKey{groupID: groupID, model: "video-model"}] = &ChannelModelPricing{
				BillingMode: tc.mode, PerRequestPrice: &price,
			}
			cache.loadedAt = time.Now()
			channels := &ChannelService{}
			channels.cache.Store(cache)
			svc.resolver = NewModelPricingResolver(channels, svc.billingService)
			key := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformOpenAI}}
			result := &OpenAIForwardResult{ResponseID: openAIVideoTaskPrefix + "2014b840-5350-4000-8000-100f38f89d5c",
				VideoCount: 1, VideoDurationSeconds: 30, VideoResolution: "720p"}
			require.InDelta(t, tc.want, svc.calculateOpenAIVideoCost(context.Background(), "video-model", key, result, 1).ActualCost, 1e-9)
		})
	}
}
