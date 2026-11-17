//go:build unit

package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

// This is an independent acceptance oracle, not a production repair.
func TestPR899IndependentVertexAPIKeyMustNotConvertToUnsupportedAnthropicAuth(t *testing.T) {
	svc, repo := newPlatformEditService()
	a := repo.getByIDAccounts[7]
	a.Platform, a.Type = PlatformGemini, AccountTypeServiceAccount
	a.Credentials = map[string]any{"auth_mode": "apikey", "api_key": "fixture-key", "location": "global", "tier_id": "vertex"}
	_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Platform: PlatformAnthropic})
	require.Error(t, err, "Anthropic Vertex only supports Service Account JSON; reject incompatible auth before writes")
	require.Empty(t, repo.updatedAccounts)
}
