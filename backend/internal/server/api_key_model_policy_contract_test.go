//go:build unit

package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyModelPolicyReadContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name        string
		mapping     map[string]string
		effort      string
		wantMapping string
	}{
		{name: "default", wantMapping: "null"},
		{name: "configured", mapping: map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}, effort: "high", wantMapping: `{"gpt-6.1-sol":"gpt-5.6-sol"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := newContractDeps(t)
			deps.apiKeyRepo.MustSeed(&service.APIKey{
				ID: 100, UserID: 1, Key: "sk_contract_owner", Name: "Owner key", Status: service.StatusActive,
				ModelMapping: tc.mapping, ReasoningEffort: tc.effort, CreatedAt: deps.now, UpdatedAt: deps.now,
			})
			deps.apiKeyRepo.MustSeed(&service.APIKey{
				ID: 101, UserID: 2, Key: "sk_contract_other_user", Status: service.StatusActive,
				ModelMapping: map[string]string{"private": "other-user-policy"}, ReasoningEffort: "ultra",
				CreatedAt: deps.now, UpdatedAt: deps.now,
			})
			status, body := doRequest(t, deps.router, http.MethodGet, "/api/v1/keys?page=1&page_size=10", "", nil)
			require.Equal(t, http.StatusOK, status, body)
			var response struct {
				Data struct {
					Items []map[string]json.RawMessage `json:"items"`
					Total int                          `json:"total"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal([]byte(body), &response))
			require.Equal(t, 1, response.Data.Total)
			require.Len(t, response.Data.Items, 1)
			item := response.Data.Items[0]
			require.JSONEq(t, "100", string(item["id"]))
			require.Contains(t, item, "model_mapping")
			require.JSONEq(t, tc.wantMapping, string(item["model_mapping"]))
			require.Contains(t, item, "reasoning_effort")
			wantEffort, err := json.Marshal(tc.effort)
			require.NoError(t, err)
			require.JSONEq(t, string(wantEffort), string(item["reasoning_effort"]))
			require.NotContains(t, body, "other-user-policy")
		})
	}
}
