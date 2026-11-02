package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLotteryInvitationConfiguration(t *testing.T) {
	c, err := normalizeLotteryInviteConfig(nil)
	require.NoError(t, err)
	require.True(t, c.DenyUnknownCountry)
	require.Equal(t, 1, c.IPDailyLimit)
	c.BlockedIPs = []string{"::ffff:203.0.113.2", "203.0.113.2", "::ffff:198.51.100.45/120", "2001:db8::4/32"}
	c.BlockedCountries = []string{" cn ", "CN", "us"}
	c, err = normalizeLotteryInviteConfig(c)
	require.NoError(t, err)
	require.Equal(t, []string{"CN", "US"}, c.BlockedCountries)
	require.Len(t, c.BlockedIPs, 3)
	for _, ip := range []string{"203.0.113.2", "::ffff:203.0.113.2", "198.51.100.1", "2001:db8:abcd::1"} {
		require.False(t, lotteryVisitAllowed(*c, ip, "JP"))
	}
	require.False(t, lotteryVisitAllowed(*c, "203.0.113.3", "CN"))
	require.False(t, lotteryVisitAllowed(*c, "203.0.113.3", ""))
	require.True(t, lotteryVisitAllowed(*c, "203.0.113.3", "JP"))
	c.DenyUnknownCountry = false
	require.True(t, lotteryVisitAllowed(*c, "127.0.0.1", ""))
	for _, ip := range []string{"garbage", "192.0.2.0/99", "fe80::1%eth0", "::ffff:192.0.2.0/80"} {
		bad := *c
		bad.BlockedIPs = []string{ip}
		_, err := normalizeLotteryInviteConfig(&bad)
		require.Error(t, err)
	}
	for _, code := range []string{"China", "1A", "?", ""} {
		bad := *c
		bad.BlockedCountries = []string{code}
		_, err := normalizeLotteryInviteConfig(&bad)
		require.Error(t, err)
	}
}
