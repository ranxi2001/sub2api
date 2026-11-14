package qqbot

import (
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

func TestSetupGroupInheritanceAndExceptions(t *testing.T) {
	c := DefaultConfig()
	c.Enabled = true
	c.AppID = "app"
	c.AllowedGroups = []string{"a", "b"}
	c.Autonomous.Enabled = true
	c.Autonomous.InheritGroups = true
	c.Moderation.Enabled = true
	c.Moderation.InheritGroups = true
	require.NoError(t, validateAutonomous(c))
	require.NoError(t, validateModeration(c))
	require.True(t, c.autonomousGroup("a"))
	require.True(t, c.moderates("a"))
	require.False(t, c.moderates("outside"))
	c.GroupPolicies = []GroupPolicy{{GroupID: "b", DisableAutonomous: true, DisableModeration: true}}
	require.False(t, c.autonomousGroup("b"))
	require.False(t, c.moderates("b"))
	c.Moderation.Groups = []string{"a", "a"}
	require.Error(t, validateModeration(c))
	c.Moderation.Groups = nil
	c.Moderation.InheritGroups = false
	require.Error(t, validateModeration(c))
	c.Autonomous.InheritGroups = false
	require.Error(t, validateAutonomous(c))
}
func TestSetupRemovedGroupsNeverExpandNotificationScope(t *testing.T) {
	c := DefaultConfig()
	c.AllowedGroups = []string{"keep"}
	c.Autonomous.Enabled = true
	c.Autonomous.Groups = []string{"removed"}
	c.PriceNotifications.Enabled = true
	c.PriceNotifications.Groups = []string{"removed"}
	c.GroupPolicies = []GroupPolicy{{GroupID: "keep", Label: "label"}, {GroupID: "removed"}}
	c.Moderation.Exemptions = []ModerationExemption{{GroupID: "removed", MemberID: "member"}}
	reconcileRemovedGroups(&c)
	require.False(t, c.PriceNotifications.Enabled)
	require.False(t, c.Autonomous.Enabled)
	require.Empty(t, c.Moderation.Exemptions)
	require.Len(t, c.GroupPolicies, 1)
}
func TestSetupBudgetValidationAndProbeFingerprint(t *testing.T) {
	c := DefaultConfig()
	for _, v := range []float64{-1, math.NaN(), math.Inf(1), 1000001, .123456789} {
		c.RewardBudgetDaily = &v
		require.Error(t, validateConfig(c))
	}
	for _, v := range []float64{0, .01, .12345678, 1000000} {
		c.RewardBudgetDaily = &v
		require.NoError(t, validateConfig(c))
	}
	c.RewardBudgetDaily = nil
	before := probeFingerprint(c)
	c.AllowedGroups = []string{"a"}
	require.Equal(t, before, probeFingerprint(c))
	c.ChatModel = "new"
	require.NotEqual(t, before, probeFingerprint(c))
	c.ChatDisabled = true
	c.ImageEnabled = false
	require.Equal(t, []string{"qq"}, requiredSetupProbes(c))
	c.ImageEnabled = true
	require.Equal(t, []string{"qq", "image", "image_edit"}, requiredSetupProbes(c))
}

func TestSetupImageConnectionModesKeepLegacyAndExplicitBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, mode, imageBase, imageKey, wantBase, wantKey string
	}{
		{"legacy-chat", "", "", "", "https://chat.test/v1", "chat-secret"},
		{"legacy-independent", "", "https://image.test/v1", "image-secret", "https://image.test/v1", "image-secret"},
		{"legacy-key-only", "", "", "image-secret", "https://chat.test/v1", "image-secret"},
		{"explicit-reuse", "reuse_chat", "https://stale.test/v1", "stale-secret", "https://chat.test/v1", "chat-secret"},
		{"explicit-site", "site", "https://image.test/v1", "image-secret", "https://image.test/v1", "image-secret"},
		{"explicit-external", "external", "https://image.test/v1", "image-secret", "https://image.test/v1", "image-secret"},
		{"explicit-external-key-only", "external", "", "image-secret", "https://chat.test/v1", "image-secret"},
		{"site-missing-does-not-fallback", "site", "", "", "", ""},
		{"external-missing-key-does-not-use-chat-key", "external", "", "", "https://chat.test/v1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultConfig()
			c.BaseURL, c.APIKey = "https://chat.test/v1", "chat-secret"
			c.ImageAIConnection, c.ImageBaseURL, c.ImageAPIKey = tc.mode, tc.imageBase, tc.imageKey
			base, key := imageConnection(c)
			require.Equal(t, tc.wantBase, base)
			require.Equal(t, tc.wantKey, key)
		})
	}
	c := DefaultConfig()
	c.ImageAIConnection = "unsupported-mode"
	require.ErrorContains(t, validateConfig(c), "图片 AI 连接类型")
	for _, mode := range []string{"", "external", "reuse_chat"} {
		c = DefaultConfig()
		c.ImageAIConnection, c.ImageSiteAPIKeyID = mode, 12
		require.ErrorContains(t, validateConfig(c), "本站图片 AI 归属信息")
	}
}

func TestSetupImageMetadataParticipatesInSectionAndProbeWithoutSecretHistory(t *testing.T) {
	old := DefaultConfig()
	old.ImageAIConnection, old.ImageSiteAPIKeyID, old.ImageSiteGroupID, old.ImageSiteOwnerID = "site", 12, 3, 7
	old.ImageBaseURL, old.ImageAPIKey = "https://image.test/v1", "image-secret"
	draft := old
	draft.ImageSiteGroupID, draft.ImageModel = 4, "gpt-image-2"
	draft.BaseURL = "https://unrelated-chat.test/v1"
	next, err := mergeConfigSection(old, draft, "images")
	require.NoError(t, err)
	require.Equal(t, int64(4), next.ImageSiteGroupID)
	require.Equal(t, "gpt-image-2", next.ImageModel)
	require.Equal(t, old.BaseURL, next.BaseURL)
	require.NotEqual(t, sectionVersion(old, "images"), sectionVersion(next, "images"))
	public := publicSection(next, "images")
	require.NotContains(t, public, "image_api_key")
	require.NotContains(t, public, "image_site_api_key_id")
	require.NotContains(t, public, "clear_image_api_key")
	before := probeFingerprint(old)
	old.ImageAIConnection = "reuse_chat"
	require.NotEqual(t, before, probeFingerprint(old))
}
