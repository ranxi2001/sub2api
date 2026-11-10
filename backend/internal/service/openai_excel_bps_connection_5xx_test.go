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
)

func managedBPSConnectionFailureFixture(t *testing.T) (*OpenAIOAuthReauthService, *connectionTypeRepo, *OpenAIAuthConnection) {
	t.Helper()
	reauth, repo := connectionTypeService(AccountTypeOAuth)
	repo.managed = true
	connection := &OpenAIAuthConnection{ID: "5d2c8a1e-3f4b-4c6d-8e9f-0a1b2c3d4e5f", AccountID: 42, Kind: "bps", Status: "active", RefreshState: "idle",
		Revision: 1, Preferred: true, Capabilities: connectionCapabilities("bps", "")}
	require.NoError(t, reauth.encryptConnectionCredentials(connection, map[string]any{
		"access_token":       "synthetic-connection-token",
		"chatgpt_account_id": "synthetic-workspace",
		"client_id":          openai.ExcelClientID,
		"expires_at":         strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
	}))
	repo.rows = []*OpenAIAuthConnection{connection}
	return reauth, repo, connection
}

func forwardManagedBPS(t *testing.T, gateway *OpenAIGatewayService, parent *Account) (*OpenAIForwardResult, error) {
	t.Helper()
	body := []byte(`{"model":"gpt-6-astra","stream":true,"input":"managed BPS failure regression"}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	return gateway.Forward(context.Background(), c, parent, body)
}

// One transient BPS server failure must fail only that request. Cooling the
// managed connection blocks every following request on the account (the
// scheduler reports capability_mismatch) although BPS has already recovered.
func TestManagedBPSConnectionTransientServerErrorKeepsConnectionSchedulable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	completed := `data: {"type":"response.completed","response":{"id":"resp_after_bps_5xx","status":"completed","model":"gpt-6-astra","output":[],"usage":{"input_tokens":10,"output_tokens":2}}}` + "\n\n"
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			reauth, repo, connection := managedBPSConnectionFailureFixture(t)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"temporarily unavailable"}}`))},
				{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(completed))},
			}}
			gateway := openAIClientToolsTestService(upstream)
			gateway.excelOAuthReauth = reauth
			parent := managedBPSBillingParent(nil)

			_, err := forwardManagedBPS(t, gateway, parent)
			require.Error(t, err, "the failed BPS request itself is still reported to the client")
			require.Equal(t, "/basispoints/api/responses", upstream.lastReq.URL.Path)
			require.Nil(t, repo.rows[0].CooldownUntil, "a transient BPS %d must not cool the managed connection", status)
			require.Equal(t, int64(1), repo.rows[0].Revision)
			require.True(t, repo.rows[0].Supports("responses"))

			result, err := forwardManagedBPS(t, gateway, parent)
			require.NoError(t, err, "the next request must still be sent through the connection")
			require.NotNil(t, result)
			require.Len(t, upstream.requests, 2)
			require.Equal(t, "Bearer synthetic-connection-token", upstream.lastReq.Header.Get("Authorization"))
			require.Equal(t, connection.ID, repo.rows[0].ID)
		})
	}
}

// Connection-specific rejections keep their existing handling.
func TestManagedBPSConnectionForbiddenStillCoolsConnection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reauth, repo, _ := managedBPSConnectionFailureFixture(t)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"forbidden"}}`))}}
	gateway := openAIClientToolsTestService(upstream)
	gateway.excelOAuthReauth = reauth
	_, err := forwardManagedBPS(t, gateway, managedBPSBillingParent(nil))
	require.Error(t, err)
	require.NotNil(t, repo.rows[0].CooldownUntil, fmt.Sprintf("BPS %d is attributed to the connection", http.StatusForbidden))
	require.False(t, repo.rows[0].Supports("responses"))
}
