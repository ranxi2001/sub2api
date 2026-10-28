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
