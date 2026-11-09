package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseSmartOpsHiddenMenuItems(t *testing.T) {
	defaults := []string{
		"/admin/lottery",
		"/admin/channel-operations",
		"/admin/controlled-experiments",
		"/admin/account-ops",
		"/admin/token-guard",
		"/admin/group-cache-tests",
		"/admin/request-captures",
		"/admin/harvest-flow",
	}
	for _, tc := range []struct {
		name string
		raw  string
		want []string
	}{
		{name: "missing", want: defaults},
		{name: "blank", raw: "  ", want: defaults},
		{name: "invalid", raw: "invalid", want: defaults},
		{name: "null", raw: "null", want: defaults},
		{name: "explicit show all", raw: "[]", want: []string{}},
		{name: "saved selection", raw: `["/admin/auto-config","/admin/future-entry","/admin/auto-config",""]`, want: []string{"/admin/auto-config", "/admin/future-entry"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, parseSmartOpsHiddenMenuItems(tc.raw))
		})
	}

	parsed := parseSmartOpsHiddenMenuItems("")
	parsed[0] = "/admin/changed"
	require.Equal(t, defaults, parseSmartOpsHiddenMenuItems(""), "defaults must not share mutable state")
}
