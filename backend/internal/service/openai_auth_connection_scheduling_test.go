//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// managedConnectionSchedulingAccount is an unprojected OAuth parent whose
// selected connection is the only route the send path will use. The legacy
// BPS switches mirror a parent that used per-model BPS before a managed BPS
// grant was selected.
func managedConnectionSchedulingAccount(id int64, kind string, capabilities ...string) Account {
	return Account{
		ID:          id,
		Name:        fmt.Sprintf("managed-%s-%d", kind, id),
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "synthetic-parent-token"},
		Extra: map[string]any{
			"openai_connections_managed": true,
			"openai_auth_connections": []map[string]any{{
				"id":            "synthetic-" + kind,
				"kind":          kind,
				"status":        "active",
				"preferred":     true,
				"capabilities":  capabilities,
				"refresh_state": "idle",
			}},
			"openai_excel_bps":        true,
			"openai_excel_bps_models": []any{"gpt-5.6-sol"},
		},
	}
}

func connectionRouteContext(path string) context.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return WithOpenAIConnectionCapabilityForRequest(context.Background(), c)
}

func TestManagedConnectionSchedulingMatchesSendPathCapability(t *testing.T) {
	bps := managedConnectionSchedulingAccount(1, "bps", "responses")
	codex := managedConnectionSchedulingAccount(2, "codex", "responses", "chat_completions", "responses_compact")
	chatOnly := Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_responses_supported": false}}
	projected := managedConnectionSchedulingAccount(4, "bps", "responses")
	projected.AuthConnectionID = "synthetic-bps"

	for _, tc := range []struct {
		name       string
		account    Account
		ctx        context.Context
		capability OpenAIEndpointCapability
		want       bool
	}{
		{"BPS ordinary Responses turn", bps, connectionRouteContext("/v1/responses"), OpenAIEndpointCapabilityChatCompletions, true},
		{"BPS bare Responses path", bps, connectionRouteContext("/responses"), OpenAIEndpointCapabilityChatCompletions, true},
		{"BPS native compaction on Responses", bps, connectionRouteContext("/v1/responses"), OpenAIEndpointCapabilityResponsesCompact, true},
		{"BPS image intent on Responses", bps, connectionRouteContext("/v1/responses"), OpenAIEndpointCapabilityResponses, true},
		{"BPS legacy compact endpoint", bps, connectionRouteContext("/v1/responses/compact"), OpenAIEndpointCapabilityResponses, false},
		{"BPS chat completions", bps, connectionRouteContext("/v1/chat/completions"), OpenAIEndpointCapabilityChatCompletions, false},
		{"BPS without route keeps literal check", bps, context.Background(), OpenAIEndpointCapabilityChatCompletions, false},
		{"BPS embeddings stay literal", bps, connectionRouteContext("/v1/responses"), OpenAIEndpointCapabilityEmbeddings, false},
		{"Codex legacy compact endpoint", codex, connectionRouteContext("/v1/responses/compact"), OpenAIEndpointCapabilityResponses, true},
		{"Codex chat completions", codex, connectionRouteContext("/v1/chat/completions"), OpenAIEndpointCapabilityChatCompletions, true},
		{"unmanaged chat-only upstream on Responses", chatOnly, connectionRouteContext("/v1/responses"), OpenAIEndpointCapabilityChatCompletions, true},
		{"unmanaged chat-only upstream needing Responses", chatOnly, connectionRouteContext("/v1/responses"), OpenAIEndpointCapabilityResponses, false},
		{"projected connection keeps its own checks", projected, connectionRouteContext("/v1/responses/compact"), OpenAIEndpointCapabilityResponses, projected.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := tc.account
			require.Equal(t, tc.want, account.supportsOpenAIEndpointCapabilityForRequest(tc.ctx, tc.capability))
		})
	}
}

func TestManagedBPSConnectionIsScheduledForResponsesTurns(t *testing.T) {
	for _, mode := range []struct {
		name      string
		advanced  bool
		loadBatch bool
	}{
		{"legacy simple", false, false},
		{"legacy load aware", false, true},
		{"advanced", true, false},
	} {
		for _, pending := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/legacy BPS pending=%v", mode.name, pending), func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
				groupID := int64(77132)
				account := managedConnectionSchedulingAccount(891, "bps", "responses")
				// The incident parent still carried a pending legacy BPS grant. A
				// non-pending legacy switch must not admit routes the selected
				// managed grant cannot serve either.
				account.Extra[ExcelBPSAuthorizationPendingKey] = pending
				cfg := &config.Config{}
				cfg.Gateway.Scheduling.LoadBatchEnabled = mode.loadBatch
				svc := &OpenAIGatewayService{
					accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
					cache:              &schedulerTestGatewayCache{},
					cfg:                cfg,
					concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
				}
				if mode.advanced {
					svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
				}
				selectFor := func(ctx context.Context, capability OpenAIEndpointCapability) (*AccountSelectionResult, error) {
					selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &groupID, "", "", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, capability, false, false, true)
					if selection != nil && selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
					return selection, err
				}

				for _, capability := range []OpenAIEndpointCapability{OpenAIEndpointCapabilityChatCompletions, OpenAIEndpointCapabilityResponsesCompact} {
					selection, err := selectFor(connectionRouteContext("/v1/responses"), capability)
					require.NoError(t, err, capability)
					require.Equal(t, int64(891), selection.Account.ID, capability)
				}

				_, err := selectFor(connectionRouteContext("/v1/chat/completions"), OpenAIEndpointCapabilityChatCompletions)
				require.True(t, errors.Is(err, ErrNoAvailableAccounts), "a BPS grant must not be offered Chat Completions: %v", err)

				_, err = selectFor(connectionRouteContext("/v1/responses/compact"), OpenAIEndpointCapabilityResponses)
				require.True(t, errors.Is(err, ErrNoAvailableAccounts), "a BPS grant must not be offered the legacy compact endpoint: %v", err)
			})
		}
	}
}

// Scheduling must admit an unprojected managed parent exactly when the send
// path can project it onto its selected connection for the same route.
func TestManagedConnectionSchedulingAgreesWithPreparation(t *testing.T) {
	routes := []struct {
		path       string
		capability OpenAIEndpointCapability
		bps        bool
	}{
		{"/v1/responses", OpenAIEndpointCapabilityChatCompletions, true},
		{"/v1/responses", OpenAIEndpointCapabilityResponsesCompact, true},
		{"/v1/responses", OpenAIEndpointCapabilityResponses, true},
		{"/v1/responses/compact", OpenAIEndpointCapabilityResponses, false},
		{"/v1/chat/completions", OpenAIEndpointCapabilityChatCompletions, false},
		{"/v1/messages", OpenAIEndpointCapabilityChatCompletions, false},
	}
	for _, kind := range []string{"bps", "codex"} {
		t.Run(kind, func(t *testing.T) {
			s, r := connectionTypeService(AccountTypeOAuth)
			r.managed = true
			connection := &OpenAIAuthConnection{ID: "6a0f1f1e-6c9f-4d55-9c3c-3f0a1b2c3d4e", AccountID: 42, Kind: kind, Status: "active", RefreshState: "idle", Revision: 1, Preferred: true, Capabilities: connectionCapabilities(kind, "")}
			require.NoError(t, s.encryptConnectionCredentials(connection, map[string]any{
				"access_token": "synthetic-grant-token",
				"expires_at":   strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
			}))
			r.rows = []*OpenAIAuthConnection{connection}
			parent, err := s.accounts.GetAccount(context.Background(), 42)
			require.NoError(t, err)
			scheduled := *parent
			scheduled.Extra = map[string]any{
				"openai_connections_managed": true,
				"openai_auth_connections": []map[string]any{{
					"id": connection.ID, "kind": kind, "status": "active", "preferred": true,
					"capabilities": connection.Capabilities, "refresh_state": "idle",
				}},
			}
			gateway := &OpenAIGatewayService{excelOAuthReauth: s, cfg: &config.Config{}}
			for _, route := range routes {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, route.path, strings.NewReader("{}"))
				projected, prepareErr := gateway.prepareAuthConnection(context.Background(), c, parent, nil, "gpt-5.6-sol")
				require.Equal(t, kind == "codex" || route.bps, prepareErr == nil, "%s %s: prepare error %v", kind, route.path, prepareErr)
				if prepareErr == nil {
					require.Equal(t, connection.ID, projected.AuthConnectionID)
				}
				admitted := scheduled.supportsOpenAIEndpointCapabilityForRequest(WithOpenAIConnectionCapabilityForRequest(context.Background(), c), route.capability)
				require.Equal(t, prepareErr == nil, admitted, "%s %s as %s: prepare error %v", kind, route.path, route.capability, prepareErr)
			}
		})
	}
}
