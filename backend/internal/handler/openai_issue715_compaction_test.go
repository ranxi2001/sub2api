//go:build unit

package handler

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Codex sends remote compaction v2 on the bare streaming /responses wire once a
// long agent session reaches its auto-compaction threshold. The history still
// carries the proven opaque agent_message items.
func issue715CompactionBody() string {
	return strings.TrimSuffix(issue715Body(true, true), "]}") + `,{"type":"compaction_trigger"}]}`
}

type issue715HeaderUpstream struct {
	*issue715Upstream
	mu      sync.Mutex
	headers []http.Header
}

func (u *issue715HeaderUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	u.mu.Lock()
	u.headers = append(u.headers, req.Header.Clone())
	u.mu.Unlock()
	return u.issue715Upstream.Do(req, proxyURL, accountID, concurrency)
}

func issue715AccountsOfType(accountType string) []service.Account {
	accounts := issue715Accounts()
	if accountType == service.AccountTypeAPIKey {
		for i := range accounts {
			accounts[i].Type = service.AccountTypeAPIKey
			accounts[i].Credentials = map[string]any{
				"api_key":  fmt.Sprintf("fixture-key-%d", accounts[i].ID),
				"base_url": "https://api.openai.com",
			}
		}
	}
	return accounts
}

func issue715PathRequest(t *testing.T, handler *OpenAIGatewayHandler, path, body string) (int, string) {
	t.Helper()
	c, rec := newIssue715Context(t, body)
	c.Request.URL.Path = path
	c.Request.Header.Set("session_id", "fixture-715-thread")
	key, _ := c.Get(string(middleware.ContextKeyAPIKey))
	key.(*service.APIKey).ID = 99
	handler.Responses(c)
	return rec.Code, rec.Body.String()
}

func TestIssue715HTTPNativeCompactionReachesProvenOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
		t.Run(accountType, func(t *testing.T) {
			accounts := issue715AccountsOfType(accountType)
			upstream := &issue715HeaderUpstream{issue715Upstream: &issue715Upstream{}}
			handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
			status, first := issue715Request(t, handler, issue715Body(false, true), 99, "fixture-715-thread", false)
			require.Equal(t, 200, status, first)
			accounts[0].Priority, accounts[1].Priority = 100, 0

			status, answer := issue715Request(t, handler, issue715CompactionBody(), 99, "fixture-715-thread", false)

			require.Equal(t, 200, status, answer)
			require.NotContains(t, answer, "encrypted_agent_recovery_")
			require.Equal(t, []int64{1, 1}, upstream.ids, "compaction must stay on the proven owner despite scheduler drift")
			sent := upstream.bodies[1]
			require.Contains(t, string(sent), "fixture-task-reference-715", "compaction must carry the intact task")
			require.Equal(t, "compaction_trigger", gjson.GetBytes(sent, "input.@reverse.0.type").String())
			require.Contains(t, upstream.headers[1].Get("x-codex-beta-features"), "remote_compaction_v2")
		})
	}
}

func TestIssue715HTTPNativeCompactionDoesNotRelaxOwnerChecks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, change := range []string{"credentials", "disabled"} {
		t.Run(change, func(t *testing.T) {
			accounts := issue715Accounts()
			upstream := &issue715Upstream{}
			handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
			status, _ := issue715Request(t, handler, issue715Body(false, true), 99, "fixture-715-thread", false)
			require.Equal(t, 200, status)
			accounts[0].Priority, accounts[1].Priority = 100, 0
			code := "encrypted_agent_recovery_route_changed"
			if change == "credentials" {
				accounts[0].Credentials = map[string]any{"access_token": "fixture-rotated"}
			} else {
				accounts[0].Schedulable = false
				code = "encrypted_agent_recovery_owner_unavailable"
			}

			status, answer := issue715Request(t, handler, issue715CompactionBody(), 99, "fixture-715-thread", false)

			require.NotEqual(t, 200, status, answer)
			require.Contains(t, answer, code)
			require.Equal(t, []int64{1}, upstream.ids, "a changed or unavailable owner must not receive or leak the compaction")
		})
	}
}

func TestIssue715HTTPLegacyCompactStaysOutsideContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := issue715Accounts()
	upstream := &issue715Upstream{}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
	status, _ := issue715Request(t, handler, issue715Body(false, false), 99, "fixture-715-thread", false)
	require.Equal(t, 200, status)

	status, answer := issue715PathRequest(t, handler, "/v1/responses/compact", issue715Body(true, false))

	require.Equal(t, http.StatusConflict, status, answer)
	require.Contains(t, answer, "encrypted_agent_recovery_route_changed")
	require.Equal(t, []int64{1}, upstream.ids)
}

func TestIssue715HTTPNativeCompactionNeverGrantsOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := issue715Accounts()
	upstream := &issue715Upstream{}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
	// No prior proof: an unseen task compacts through ordinary admission and
	// must not turn that compaction into an ownership observation.
	status, answer := issue715Request(t, handler, issue715CompactionBody(), 99, "fixture-715-thread", false)
	require.Equal(t, 200, status, answer)
	require.Equal(t, []int64{1}, upstream.ids)
	accounts[0].Priority, accounts[1].Priority = 100, 0

	issue715Request(t, handler, issue715Body(true, true), 99, "fixture-715-thread", false)

	require.GreaterOrEqual(t, len(upstream.ids), 2)
	require.Equal(t, int64(2), upstream.ids[1], "compaction is not a compatibility observation")
}
