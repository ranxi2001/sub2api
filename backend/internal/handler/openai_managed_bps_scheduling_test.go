//go:build unit

package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// A selected managed BPS grant advertises native Responses only. The ordinary
// /v1/responses route must still offer that parent to the send path; before
// the route capability reached account selection it was filtered as a
// capability mismatch and the lower-priority account served the turn.
func TestOpenAIResponsesRouteOffersManagedBPSParent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	managed := service.Account{
		ID: 1, Name: "managed-bps", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Priority: 0,
		Credentials: map[string]any{"access_token": "fixture-parent-token"},
		Extra: map[string]any{
			"openai_connections_managed": true,
			"openai_auth_connections": []map[string]any{{
				"id": "fixture-bps-connection", "kind": "bps", "status": "active", "preferred": true,
				"capabilities": []string{"responses"}, "refresh_state": "idle",
			}},
		},
	}
	ordinary := service.Account{
		ID: 2, Name: "ordinary", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Priority: 5,
		Credentials: map[string]any{"access_token": "fixture-ordinary-token"},
	}
	handler := newOpenAIResponsesFailoverTestHandler(t, &issue715Upstream{}, managed, ordinary)
	c, _ := newIssue715Context(t, `{"model":"gpt-5.1","stream":true,"input":[{"type":"message","role":"user","content":"hello"}]}`)

	handler.Responses(c)

	selected, ok := c.Get(opsAccountIDKey)
	require.True(t, ok, "the route must reach account selection")
	require.Equal(t, int64(1), selected, "the managed BPS parent must be offered to the send path")
}
