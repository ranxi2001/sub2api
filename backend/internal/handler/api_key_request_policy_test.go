package handler

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAPIKeyUsageFieldsPreserveActualRewriteAndChannelProvenance(t *testing.T) {
	const original, keyTarget = "gpt-6.1-sol", "gpt-5.6-sol"
	for _, tc := range []struct {
		name           string
		applied        string
		channelMapping service.ChannelMappingResult
	}{
		{name: "unmapped request"},
		{name: "key target", applied: keyTarget},
		{name: "explicit channel back to original", applied: keyTarget, channelMapping: service.ChannelMappingResult{Mapped: true, MappedModel: original}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx := context.WithValue(context.Background(), ctxkey.RequestedPublicModel, original)
			ctx = service.WithAPIKeyMappedModel(ctx, tc.applied)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			fields := clientRequestedUsageFields(c, tc.channelMapping, keyTarget, "upstream")
			require.Equal(t, original, fields.OriginalModel)
			require.Equal(t, tc.applied, fields.APIKeyMappedModel)
			require.Equal(t, tc.channelMapping.Mapped, fields.ChannelMappingApplied)
			require.Equal(t, original, fields.ChannelMappedModel)
		})
	}
	for _, c := range []*gin.Context{nil, {}} {
		fields := clientRequestedUsageFields(c, service.ChannelMappingResult{}, original, "upstream")
		require.Equal(t, original, fields.OriginalModel)
		require.Empty(t, fields.APIKeyMappedModel)
	}
}

func TestAPIKeyReasoningPolicyHandlerScopesAndRequestedAudit(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","reasoning":{"effort":"low"}}`)
	for _, tc := range []struct{ platform, fixed, want string }{
		{service.PlatformOpenAI, "high", "high"},
		{service.PlatformOpenAI, "", "low"},
		{service.PlatformGrok, "high", "low"},
		{service.PlatformAnthropic, "high", "low"},
	} {
		t.Run(tc.platform+tc.fixed, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			key := &service.APIKey{ReasoningEffort: tc.fixed, Group: &service.Group{Platform: tc.platform}}
			got, _, err := applyOpenAIReasoningEffortPolicyForRequest(c, key, body)
			require.NoError(t, err)
			require.Equal(t, tc.want, gjson.GetBytes(got, "reasoning.effort").String())
			require.Equal(t, "low", *service.RequestedReasoningEffortFromContext(c.Request.Context()))
		})
	}
}

func TestAPIKeyReasoningPolicyMessagesConversionIncludesOmittedEffort(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	key := &service.APIKey{ReasoningEffort: "high", Group: &service.Group{Platform: service.PlatformOpenAI, MaxReasoningEffort: "medium"}}
	_, err := prepareOpenAIReasoningEffortPolicyForMessagesRequest(c, key, []byte(`{"model":"gpt-5.6-sol","messages":[]}`))
	require.NoError(t, err)
	converted, changed, err := service.ApplyOpenAIReasoningEffortPolicyFromContext(c.Request.Context(), []byte(`{"model":"gpt-5.6-sol","input":[],"reasoning":{"effort":"low"}}`))
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "medium", gjson.GetBytes(converted, "reasoning.effort").String())
}
