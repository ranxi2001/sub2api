package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBalanceForecastRetiredNotification(t *testing.T) {
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	var hasLowBalance bool
	for _, event := range svc.ListEventInfos() {
		require.NotEqual(t, "balance.forecast", event.Event)
		hasLowBalance = hasLowBalance || event.Event == NotificationEmailEventBalanceLow
	}
	require.True(t, hasLowBalance)
	_, err := svc.GetTemplate(context.Background(), "balance.forecast", "zh")
	require.Error(t, err, "retired event cannot be previewed or sent")
	_, err = svc.GetTemplate(context.Background(), NotificationEmailEventBalanceLow, "zh")
	require.NoError(t, err, "amount-based notification remains supported")
}
