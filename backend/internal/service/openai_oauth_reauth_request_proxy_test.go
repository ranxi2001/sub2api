package service

import (
	"context"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	redissessiontest "github.com/Wei-Shaw/sub2api/internal/testutil/redissession"
	"github.com/stretchr/testify/require"
)

type reauthRequestProxyRepo struct {
	ProxyRepository
	proxies map[int64]*Proxy
}

func (r *reauthRequestProxyRepo) GetByID(_ context.Context, id int64) (*Proxy, error) {
	return r.proxies[id], nil
}

func TestOpenAIOAuthReauthRequestProxyKeepsSelectedExitThroughCallback(t *testing.T) {
	svc, reader, _, _, client, _ := newReauthTestService("acct-1")
	t.Cleanup(svc.oauth.Stop)
	ctx := context.Background()
	legacyID := int64(90)
	reader.account.ProxyID = &legacyID
	reader.account.Extra = map[string]any{
		"openai_proxy_mode": "request",
		"openai_proxy_ids":  []any{float64(1), float64(2)},
	}
	reader.proxy = &Proxy{ID: legacyID, Status: StatusActive, Protocol: "http", Host: "outside.example", Port: 8080}
	proxies := &reauthRequestProxyRepo{proxies: map[int64]*Proxy{
		2: {ID: 2, Status: StatusActive, Protocol: "http", Host: "selected.example", Port: 8080},
	}}
	svc.oauth.proxyRepo = proxies
	_, err := svc.SaveConfig(ctx, 42, "user@example.com", "https://mail.example.com/code")
	require.NoError(t, err)
	_, err = svc.CreateTask(ctx, 42)
	require.NoError(t, err)

	claim, err := svc.ClaimTask(ctx, "worker-a")
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Equal(t, "http://selected.example:8080", claim.ProxyURL)
	// A different member becoming available must not move an authorization
	// session between the browser step and its code exchange.
	proxies.proxies[1] = &Proxy{ID: 1, Status: StatusActive, Protocol: "http", Host: "earlier.example", Port: 8080}
	state := reauthStateFromURL(t, claim.AuthURL)
	task, err := svc.SubmitCallback(ctx, claim.TaskID, "worker-a", "http://localhost:1455/auth/callback?code=test-code&state="+url.QueryEscape(state))
	require.NoError(t, err)
	require.Equal(t, OpenAIOAuthReauthStatusSucceeded, task.Status)
	require.Equal(t, "http://selected.example:8080", client.proxyURL)
	require.Equal(t, legacyID, *reader.account.ProxyID)
}

func TestOpenAIOAuthReauthRequestProxyCallbackRejectsRemovedExit(t *testing.T) {
	svc, reader, _, updater, client, _ := newReauthTestService("acct-1")
	t.Cleanup(svc.oauth.Stop)
	ctx := context.Background()
	reader.account.Extra = map[string]any{
		"openai_proxy_mode": "request",
		"openai_proxy_ids":  []any{float64(2)},
	}
	svc.oauth.proxyRepo = &reauthRequestProxyRepo{proxies: map[int64]*Proxy{
		2: {ID: 2, Status: StatusActive, Protocol: "http", Host: "selected.example", Port: 8080},
		3: {ID: 3, Status: StatusActive, Protocol: "http", Host: "replacement.example", Port: 8080},
	}}
	_, err := svc.SaveConfig(ctx, 42, "user@example.com", "https://mail.example.com/code")
	require.NoError(t, err)
	_, err = svc.CreateTask(ctx, 42)
	require.NoError(t, err)
	claim, err := svc.ClaimTask(ctx, "worker-a")
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Equal(t, "http://selected.example:8080", claim.ProxyURL)

	reader.account.Extra["openai_proxy_ids"] = []any{float64(3)}
	state := reauthStateFromURL(t, claim.AuthURL)
	_, err = svc.SubmitCallback(ctx, claim.TaskID, "worker-a", "http://localhost:1455/auth/callback?code=test-code&state="+url.QueryEscape(state))
	require.ErrorContains(t, err, "authorization proxy is no longer in the available account proxy group")
	status, statusErr := svc.GetStatus(ctx, 42)
	require.NoError(t, statusErr)
	require.Equal(t, OpenAIOAuthReauthStatusFailed, status.Task.Status)
	require.False(t, updater.applied)
	require.Zero(t, client.exchanges, "an authorization session must not escape the current selected group")
}

func TestOpenAIOAuthReauthRequestProxyCallbackRejectsPreviousSingleModeExit(t *testing.T) {
	for _, legacyProxy := range []bool{false, true} {
		name := "direct"
		if legacyProxy {
			name = "outside_proxy"
		}
		t.Run(name, func(t *testing.T) {
			svc, reader, _, updater, client, _ := newReauthTestService("acct-1")
			t.Cleanup(svc.oauth.Stop)
			ctx := context.Background()
			if legacyProxy {
				id := int64(90)
				reader.account.ProxyID = &id
				reader.proxy = &Proxy{ID: id, Status: StatusActive, Protocol: "http", Host: "outside.example", Port: 8080}
			}
			_, err := svc.SaveConfig(ctx, 42, "user@example.com", "https://mail.example.com/code")
			require.NoError(t, err)
			_, err = svc.CreateTask(ctx, 42)
			require.NoError(t, err)
			claim, err := svc.ClaimTask(ctx, "worker-a")
			require.NoError(t, err)
			require.NotNil(t, claim)
			reader.account.Extra = map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []any{float64(2)}}
			svc.oauth.proxyRepo = &reauthRequestProxyRepo{proxies: map[int64]*Proxy{
				2: {ID: 2, Status: StatusActive, Protocol: "http", Host: "selected.example", Port: 8080},
			}}
			state := reauthStateFromURL(t, claim.AuthURL)
			_, err = svc.SubmitCallback(ctx, claim.TaskID, "worker-a", "http://localhost:1455/auth/callback?code=test-code&state="+url.QueryEscape(state))
			require.ErrorContains(t, err, "authorization proxy is no longer in the available account proxy group")
			status, statusErr := svc.GetStatus(ctx, 42)
			require.NoError(t, statusErr)
			require.Equal(t, OpenAIOAuthReauthStatusFailed, status.Task.Status)
			require.False(t, updater.applied)
			require.Zero(t, client.exchanges)
		})
	}
}

func TestOpenAIOAuthReauthRequestProxyCallbackUsesSharedSIWCSession(t *testing.T) {
	for _, removeExit := range []bool{false, true} {
		name := "available"
		if removeExit {
			name = "removed"
		}
		t.Run(name, func(t *testing.T) {
			sessionStore := redissessiontest.New(t, siwcSessionPrefix, openai.SessionTTL)
			svc, reader, repo, _, oldClient, _ := newReauthTestService("acct-1")
			t.Cleanup(svc.oauth.Stop)
			store := &siwcTestRepo{reauthTestRepo: repo}
			svc.repo = store
			svc.siwcSessionStore = sessionStore
			client := &siwcTestClient{reauthTestOAuthClient: oldClient}
			svc.oauth.oauthClient = client
			reader.account.Extra = map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []any{float64(2)}}
			svc.oauth.proxyRepo = &reauthRequestProxyRepo{proxies: map[int64]*Proxy{
				2: {ID: 2, Status: StatusActive, Protocol: "http", Host: "selected.example", Port: 8080},
				3: {ID: 3, Status: StatusActive, Protocol: "http", Host: "replacement.example", Port: 8080},
			}}
			ctx := context.Background()
			savePasswordReauthConfig(t, svc)
			_, err := svc.CreateTaskForProfile(ctx, 42, "siwc")
			require.NoError(t, err)
			claim, err := svc.ClaimTask(ctx, "worker-a")
			require.NoError(t, err)
			require.NotNil(t, claim)
			require.Equal(t, "http://selected.example:8080", claim.ProxyURL)
			if removeExit {
				reader.account.Extra["openai_proxy_ids"] = []any{float64(3)}
			}
			state := reauthStateFromURL(t, claim.AuthURL)
			_, err = svc.SubmitCallback(ctx, claim.TaskID, "worker-a", "http://127.0.0.1:1455/auth/callback?code=test-code&state="+url.QueryEscape(state)+"&client_id=issued")
			if removeExit {
				require.ErrorContains(t, err, "authorization proxy is no longer in the available account proxy group")
				status, statusErr := svc.GetStatus(ctx, 42)
				require.NoError(t, statusErr)
				require.Equal(t, OpenAIOAuthReauthStatusFailed, status.Task.Status)
				require.Zero(t, client.calls)
				require.Zero(t, store.writes)
				return
			}
			require.NoError(t, err)
			status, statusErr := svc.SIWCStatus(ctx, 42)
			require.NoError(t, statusErr)
			require.True(t, status.Bound)
			require.Equal(t, 1, client.calls)
			require.Equal(t, 1, store.writes)
		})
	}
}
