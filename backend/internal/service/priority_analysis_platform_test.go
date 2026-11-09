package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type priorityAnalysisCompositeChannel struct{ ChannelRepository }

func (*priorityAnalysisCompositeChannel) GetChannelIDByGroupID(context.Context, int64) (int64, error) {
	return 1, nil
}
func (*priorityAnalysisCompositeChannel) GetByID(context.Context, int64) (*Channel, error) {
	return &Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{17}, ModelMapping: map[string]map[string]string{PlatformAnthropic: {"shared-alias": "claude-route"}, PlatformOpenAI: {"shared-alias": "openai-route"}}}, nil
}
func (*priorityAnalysisCompositeChannel) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{17: PlatformComposite}, nil
}

func TestPriorityAnalysisCompositeAliasStaysInOpenAIScope(t *testing.T) {
	svc := &OpenAIGatewayService{channelService: &ChannelService{repo: &priorityAnalysisCompositeChannel{}}}
	lookup, err := svc.priorityAnalysisChannel(context.Background(), 17)
	require.NoError(t, err)
	require.Equal(t, "openai-route", resolveMapping(lookup, 17, "shared-alias").MappedModel, "OpenAI analysis must not resolve an Anthropic alias from the same composite group")
}
