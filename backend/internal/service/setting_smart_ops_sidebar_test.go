package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseSmartOpsHiddenMenuItems(t *testing.T) {
	defaults := []string{
		"/admin/lottery",
		"/admin/qqbot/overview",
		"/admin/controlled-experiments",
		"/admin/group-cache-tests",
		"/admin/request-captures",
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
		{name: "partially hidden legacy entries", raw: `["/admin/channel-operations","/admin/account-ops","/admin/future"]`, want: []string{"/admin/future"}},
		{name: "all legacy entries hidden", raw: `["/admin/profit-operations","/admin/account-ops","/admin/channel-operations"]`, want: []string{}},
		{name: "explicit merged selection", raw: `["/admin/channel-config","/admin/account-ops"]`, want: []string{"/admin/channel-config"}},
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
