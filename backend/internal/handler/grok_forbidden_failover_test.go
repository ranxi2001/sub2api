package handler

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGrokForbiddenFailoverBudget(t *testing.T) {
	unknown := &service.UpstreamFailoverError{StatusCode: http.StatusForbidden, Reason: service.GrokUnknownForbiddenReason}
	t.Run("one alternate then stop even on a different error", func(t *testing.T) {
		var budget grokForbiddenFailoverBudget
		require.True(t, budget.canRetry(unknown, 0))
		require.False(t, budget.canRetry(&service.UpstreamFailoverError{StatusCode: http.StatusBadGateway}, 1))
	})
	t.Run("budget begins at first unknown refusal", func(t *testing.T) {
		var budget grokForbiddenFailoverBudget
		require.True(t, budget.canRetry(&service.UpstreamFailoverError{StatusCode: http.StatusUnauthorized}, 0))
		require.True(t, budget.canRetry(unknown, 3))
		require.False(t, budget.canRetry(unknown, 4))
	})
	t.Run("legacy and credential stop unchanged", func(t *testing.T) {
		var budget grokForbiddenFailoverBudget
		require.True(t, budget.canRetry(&service.UpstreamFailoverError{StatusCode: http.StatusForbidden}, 10))
		require.False(t, budget.canRetry(&service.UpstreamFailoverError{NextAccountAction: service.NextAccountStop}, 0))
	})
}
