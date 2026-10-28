package service

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestUpstreamOperationsIdentityBoundaries(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"HTTPS://RELAY.EXAMPLE:8443/Tenant/v1/", "https://relay.example:8443/Tenant/v1"},
		{"https://relay.example/Other/v1", "https://relay.example/Other/v1"},
	} {
		got, status := ResolveOperationsUpstream(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": tc.raw}})
		require.Equal(t, tc.want, got, "effective API base paths and ports must survive normalization")
		require.Equal(t, "current_config", status)
	}
	got, _ := ResolveOperationsUpstream(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
	require.Equal(t, "https://api.openai.com", got, "use actual forwarding default")
	for _, raw := range []string{"https://user:secret@relay.example/v1?token=secret#secret", "not a URL"} {
		got, status := ResolveOperationsUpstream(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": raw}})
		require.Empty(t, got, "unsafe URL must not leak or masquerade as an established upstream")
		require.NotEqual(t, "current_config", status)
	}
}
func TestUpstreamOperationsRejectUnboundedAndInvalidQueries(t *testing.T) {
	now := time.Now().UTC()
	valid := UpstreamOperationsFilter{From: now.Add(-24 * time.Hour), Until: now, Dimension: "upstream", ModelScope: "public", Sort: "requests", Direction: "desc", Page: 1, PageSize: 20}
	require.NoError(t, ValidateUpstreamOperationsFilter(valid))
	for _, change := range []func(*UpstreamOperationsFilter){func(f *UpstreamOperationsFilter) { f.From = time.Time{} }, func(f *UpstreamOperationsFilter) { f.From = now.Add(-32 * 24 * time.Hour) }, func(f *UpstreamOperationsFilter) { f.PageSize = 101 }, func(f *UpstreamOperationsFilter) { f.Sort = "actual_cost;DROP TABLE usage_logs" }, func(f *UpstreamOperationsFilter) { f.AccountID = -1 }, func(f *UpstreamOperationsFilter) { f.Until = f.From }} {
		f := valid
		change(&f)
		require.Error(t, ValidateUpstreamOperationsFilter(f), "invalid query must fail before SQL")
	}
}

func TestUpstreamOperationsOAuthAmbiguityAndMultiplierSources(t *testing.T) {
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"base_url": "https://ignored.example"}}
	got, status := ResolveOperationsUpstream(oauth)
	require.Equal(t, "https://chatgpt.com/backend-api/codex", got)
	require.Equal(t, "current_config", status)
	oauth.Extra = map[string]any{OpenAISIWCEnabledKey: true}
	got, status = ResolveOperationsUpstream(oauth)
	require.Empty(t, got)
	require.Equal(t, "oauth_dynamic_endpoint", status)
	multi := &Account{Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_protocol": "adaptive", "api_base_urls": map[string]any{"chat_completions": "https://relay.example/v1", "anthropic": "https://relay.example/anthropic", "responses": "https://relay.example/v1"}}}
	got, status = ResolveOperationsUpstream(multi)
	require.Empty(t, got)
	require.Equal(t, "multiple_effective_bases", status)
	missing, basis := OperationsCostMultiplier(nil)
	require.Nil(t, missing)
	require.Equal(t, "account_missing", basis)
	for _, tc := range []struct {
		extra map[string]any
		want  *float64
		basis string
	}{
		{map[string]any{}, ptrOps(.1), "current_default_0.1"},
		{map[string]any{"cost_multiplier": 0.0}, ptrOps(0), "current_saved_multiplier"},
		{map[string]any{"cost_multiplier": -1}, nil, "invalid_multiplier"},
		{map[string]any{"cost_multiplier": "0.25"}, nil, "invalid_multiplier"},
	} {
		v, b := OperationsCostMultiplier(&Account{Extra: tc.extra})
		require.Equal(t, tc.want, v)
		require.Equal(t, tc.basis, b)
	}
}
func ptrOps(v float64) *float64 { return &v }
