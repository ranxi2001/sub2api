package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin/binding"
	"github.com/stretchr/testify/require"
)

func TestGatewayDataImportIdentityValidation(t *testing.T) {
	item := DataAccount{Name: "Gateway import fixture", Platform: service.PlatformGateway, Type: service.AccountTypeGateway,
		Credentials: map[string]any{"upstream_kind": "sub2api", "base_url": "https://import.example", "api_key": "synthetic-key"}}
	require.NoError(t, validateDataAccount(item))
	require.NoError(t, binding.Validator.ValidateStruct(CreateAccountRequest{Name: item.Name, Platform: item.Platform, Type: item.Type, Credentials: item.Credentials}))
	require.NoError(t, binding.Validator.ValidateStruct(UpdateAccountRequest{Type: item.Type}))
	item.Platform = service.PlatformOpenAI
	require.Error(t, validateDataAccount(item))
	item.Platform = service.PlatformGateway
	item.Credentials["upstream_kind"] = "unknown"
	require.Error(t, validateDataAccount(item))
	item.Type = service.AccountTypeAPIKey
	require.Error(t, validateDataAccount(item))
}
