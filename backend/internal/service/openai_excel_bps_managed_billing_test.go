package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A managed BPS parent keeps the legacy authorization marker because it never
// receives a legacy Excel grant. The request is sent with the projected
// connection account, but usage is recorded with the scheduled parent.
func managedBPSBillingParent(option any) *Account {
	extra := map[string]any{
		"openai_excel_bps":              true,
		ExcelBPSAuthorizationPendingKey: true,
		"openai_connections_managed":    true,
		"openai_excel_bps_mihomo":       false,
		"openai_excel_bps_models":       []any{"gpt-6-astra"},
	}
	if option != nil {
		extra["openai_excel_bps_cache_creation_as_input"] = option
	}
	return &Account{ID: 42, Name: "managed-bps", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 10,
		Credentials: map[string]any{"access_token": "synthetic-legacy-token", "chatgpt_account_id": "synthetic-workspace"}, Extra: extra}
}

func managedBPSBillingUsageService() (*OpenAIGatewayService, *openAIRecordUsageLogRepoStub, *openAIRecordUsageBillingRepoStub) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	svc.billingService = NewBillingService(svc.cfg, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-6-astra": {
			InputCostPerToken: 5e-6, OutputCostPerToken: 30e-6,
			CacheCreationInputTokenCost: 6.25e-6, CacheCreationInputTokenCostExplicit: true,
			CacheReadInputTokenCost: 0.5e-6,
		},
	}})
	return svc, usageRepo, billingRepo
}

func TestManagedBPSConnectionBillsCacheCreationAsShownToClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, option := range []bool{true, false} {
			t.Run(fmt.Sprintf("stream=%t/option=%t", stream, option), func(t *testing.T) {
				reauth, repo := connectionTypeService(AccountTypeOAuth)
				repo.managed = true
				connection := &OpenAIAuthConnection{ID: "2b6f0f4e-7c3d-4b8e-9f21-5a7c9d1e3f40", AccountID: 42, Kind: "bps", Status: "active", RefreshState: "idle",
					Revision: 1, Preferred: true, Capabilities: connectionCapabilities("bps", "")}
				require.NoError(t, reauth.encryptConnectionCredentials(connection, map[string]any{
					"access_token":       "synthetic-connection-token",
					"chatgpt_account_id": "synthetic-workspace",
					"client_id":          openai.ExcelClientID,
					"expires_at":         strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
				}))
				repo.rows = []*OpenAIAuthConnection{connection}

				parent := managedBPSBillingParent(option)
				require.False(t, parent.IsExcelBPSEnabled(), "fixture must carry the legacy authorization marker")

				wire := `data: {"type":"response.completed","response":{"id":"resp_managed_bps_usage","status":"completed","model":"gpt-6-astra","output":[],"usage":` + excelBPSUsageWithCreation + `}}` + "\n\n"
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
				gateway := openAIClientToolsTestService(upstream)
				gateway.excelOAuthReauth = reauth
				body := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","stream":%t,"input":"managed billing regression"}`, stream))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

				result, err := gateway.Forward(context.Background(), c, parent, body)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, "/basispoints/api/responses", upstream.lastReq.URL.Path)
				require.Equal(t, "Bearer synthetic-connection-token", upstream.lastReq.Header.Get("Authorization"), "the request must be sent through the managed connection")
				require.Equal(t, connection.ID, c.GetString("openai_auth_connection_id"))
				require.Equal(t, 200, result.Usage.CacheCreationInputTokens, "retain the upstream measurement for local billing")

				response := rec.Body.Bytes()
				if stream {
					for _, line := range strings.Split(rec.Body.String(), "\n") {
						if strings.HasPrefix(line, "data: ") {
							response = []byte(gjson.Get(strings.TrimPrefix(line, "data: "), "response").Raw)
						}
					}
				}
				shown, ok := extractOpenAIUsageFromJSONBytes(response)
				require.True(t, ok)
				wantInput, wantCreation := 700, 200
				if option {
					wantInput, wantCreation = 900, 0
				}
				require.Equal(t, wantCreation, shown.CacheCreationInputTokens)

				usageSvc, usageRepo, billingRepo := managedBPSBillingUsageService()
				require.NoError(t, usageSvc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
					Result: result, APIKey: &APIKey{ID: 1001}, User: &User{ID: 2001}, Account: parent,
				}))
				require.NotNil(t, usageRepo.lastLog)
				log := usageRepo.lastLog
				require.Equal(t, shown.CacheCreationInputTokens, log.CacheCreationTokens, "billing must match the usage returned to the client")
				require.Equal(t, wantInput, log.InputTokens)
				require.Equal(t, wantCreation, log.CacheCreationTokens)
				require.Equal(t, 100, log.CacheReadTokens)
				require.InDelta(t, float64(wantInput)*5e-6, log.InputCost, 1e-12)
				require.InDelta(t, float64(wantCreation)*6.25e-6, log.CacheCreationCost, 1e-12)
				require.Equal(t, 1, billingRepo.calls)
				require.Equal(t, wantInput, billingRepo.lastCmd.InputTokens)
				require.Equal(t, wantCreation, billingRepo.lastCmd.CacheCreationTokens)
			})
		}
	}
}

func TestExcelBPSCacheCreationBilledAsInputScope(t *testing.T) {
	bpsResult := &OpenAIForwardResult{UpstreamEndpoint: "/basispoints/api/responses"}
	legacy := func(option any) *Account {
		extra := map[string]any{"openai_excel_bps": true}
		if option != nil {
			extra["openai_excel_bps_cache_creation_as_input"] = option
		}
		return &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: extra}
	}
	for _, tt := range []struct {
		name    string
		account *Account
		result  *OpenAIForwardResult
		want    bool
	}{
		{"legacy BPS enabled", legacy(true), bpsResult, true},
		{"legacy BPS option off", legacy(false), bpsResult, false},
		{"legacy BPS option missing", legacy(nil), bpsResult, false},
		{"legacy BPS pending keeps option hidden", func() *Account {
			a := legacy(true)
			a.Extra[ExcelBPSAuthorizationPendingKey] = true
			return a
		}(), bpsResult, false},
		{"legacy BPS disabled", func() *Account { a := legacy(true); a.Extra["openai_excel_bps"] = false; return a }(), bpsResult, false},
		{"managed parent option on", managedBPSBillingParent(true), bpsResult, true},
		{"managed parent option off", managedBPSBillingParent(false), bpsResult, false},
		{"managed parent option missing", managedBPSBillingParent(nil), bpsResult, false},
		{"managed parent invalid option", managedBPSBillingParent("true"), bpsResult, false},
		{"managed parent native result", managedBPSBillingParent(true), &OpenAIForwardResult{UpstreamEndpoint: "/v1/responses"}, false},
		{"legacy native result", legacy(true), &OpenAIForwardResult{UpstreamEndpoint: "/v1/responses"}, false},
		{"nil account", nil, bpsResult, false},
		{"nil result", managedBPSBillingParent(true), nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, excelBPSCacheCreationBilledAsInput(tt.account, tt.result))
		})
	}
}
