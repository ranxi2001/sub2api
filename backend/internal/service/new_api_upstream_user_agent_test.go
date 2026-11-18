package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type newAPIUserAgentClient struct {
	HTTPUpstream
	client *http.Client
}

func (h *newAPIUserAgentClient) DoWithTLS(r *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return h.client.Do(r)
}

func TestNewAPIQueryUserAgentPassesGatewayAndPreservesOverrides(t *testing.T) {
	for _, tc := range []struct {
		name        string
		credentials map[string]any
		want        string
	}{
		{name: "default", want: "Sub2API-Upstream-Probe/1.0"},
		{name: "explicit override", credentials: map[string]any{
			"header_override_enabled": true,
			"header_overrides":        map[string]any{"User-Agent": "Custom-Upstream-Client/2.0"},
		}, want: "Custom-Upstream-Client/2.0"},
		{name: "disabled override", credentials: map[string]any{
			"header_override_enabled": false,
			"header_overrides":        map[string]any{"User-Agent": "Disabled-Client/1.0"},
		}, want: "Sub2API-Upstream-Probe/1.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := make(chan http.Header, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers <- r.Header.Clone()
				// Reproduce an upstream gateway accepting the first three queries,
				// but returning HTML 403 for Go's default UA on token inventory.
				if r.URL.Path == "/api/token/" && r.UserAgent() == "Go-http-client/1.1" {
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/status":
					_, _ = io.WriteString(w, `{"success":true,"data":{"quota_per_unit":500000}}`)
				case "/api/user/self":
					_, _ = io.WriteString(w, `{"success":true,"data":{"id":7,"quota":1250000}}`)
				case "/api/pricing":
					_, _ = io.WriteString(w, `{"success":true,"group_ratio":{"vip":0.5}}`)
				case "/api/token/":
					_, _ = io.WriteString(w, `{"success":true,"data":{"items":[{"id":3,"key":"fixture","group":"vip"}],"total":1,"page":1,"page_size":100}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			svc := newUpstreamBillingProbeTestService(nil, &newAPIUserAgentClient{client: server.Client()}, nil)
			data, err := svc.fetchNewAPI(context.Background(), &Account{ID: 1, Platform: PlatformKimi, Type: AccountTypeAPIKey, Credentials: tc.credentials}, server.URL, 7, "fixture-system-token")
			require.NoError(t, err)
			require.Equal(t, 2.5, data.wallet)
			require.Equal(t, 0.5, data.ratios["vip"])
			require.Len(t, data.tokens, 1)
			require.Len(t, headers, 4)
			for i := 0; i < 4; i++ {
				h := <-headers
				require.Equal(t, tc.want, h.Get("User-Agent"))
				require.Equal(t, "Bearer fixture-system-token", h.Get("Authorization"))
				require.Equal(t, "7", h.Get("New-Api-User"))
			}
		})
	}
}
