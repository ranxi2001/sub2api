//go:build unit

package adminaccess

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAdminFullAccessRouteBoundary(t *testing.T) {
	var policy Policy
	if err := json.Unmarshal([]byte(`{"full_access":true}`), &policy); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path    string
		allowed bool
	}{
		{"/api/v1/admin/future-operation", true},
		{"/api/v1/admin/accounts/:id", true},
		{"/api/v1/admin/settings/admin-api-keys", false},
		{"/api/v1/admin/settings/admin-api-keys/:id/rotate", false},
		{"/api/v1/admin/settings/admin-api-key/regenerate", false},
		{"/api/v1/user/profile", false},
		{"/v1/messages", false},
		{"", false},
	} {
		if got := RouteAllowed(policy, "POST", tc.path); got != tc.allowed {
			t.Errorf("%s: got %v want %v", tc.path, got, tc.allowed)
		}
	}
}

func TestFullAccessRetainsIdentityWithoutResourceRestrictions(t *testing.T) {
	principal := &Principal{KeyID: "same-key", Policy: Policy{FullAccess: true}}
	ctx := WithPrincipal(context.Background(), principal)
	require.Same(t, principal, FromContext(ctx))
	require.Nil(t, RestrictedFromContext(ctx))
	principal.Policy.FullAccess = false
	require.Same(t, principal, RestrictedFromContext(ctx))
	require.Nil(t, RestrictedFromContext(context.Background()))
	require.False(t, RouteAllowed(principal.Policy, "POST", "/api/v1/admin/accounts/data"))
	require.False(t, RouteAllowed(principal.Policy, "GET", "/api/v1/admin/openai/accounts/:id/quota"))
}
