package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type priorityAnalysisChannelFailure struct{ ChannelRepository }

func (*priorityAnalysisChannelFailure) GetChannelIDByGroupID(context.Context, int64) (int64, error) {
	return 0, errors.New("fixture channel database unavailable")
}
func (*priorityAnalysisChannelFailure) ListAll(context.Context) ([]Channel, error) {
	return nil, errors.New("fixture channel database unavailable")
}

func TestPriorityAnalysisMappingErrorCannotBecomeEmptySuccess(t *testing.T) {
	channels := &ChannelService{repo: &priorityAnalysisChannelFailure{}}
	// Routing intentionally caches an empty fallback after DB errors. A read-only
	// diagnostic must not treat that fallback as an authoritative empty catalog.
	channels.storeErrorCache()
	svc := &OpenAIGatewayService{accountRepo: &analysisAccounts{}, channelService: channels}
	group := int64(17)
	models, err := svc.PriorityAnalysisModels(context.Background(), &group)
	require.Error(t, err, "a failed mapping read must never become an empty successful catalog")
	require.Nil(t, models)
}
