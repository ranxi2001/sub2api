// Adapted from mizaawa/zayuapi a71aa9615ccb55a19973908c9f261b5ba25a48c6 (LGPL v3).
// See mature_provider_NOTICE.md and docs/mature-provider-provenance.json.
package admin

import (
	"github.com/stretchr/testify/require"
	"net/netip"
	"testing"
)

func TestMatureProtocolAllowedProviderIPRejectsSpecialPurposeAndTranslationRanges(t *testing.T) {
	for _, raw := range []string{
		"10.0.0.1", "127.0.0.1", "169.254.169.254", "100.64.0.1", "0.1.2.3", "192.0.2.10",
		"198.51.100.7", "203.0.113.9", "240.0.0.1", "::ffff:10.0.0.1", "64:ff9b::a00:1",
		"2002:a00:1::1", "2001:0:4136:e378::1", "fd00::1", "fe80::1", "::1",
	} {
		require.False(t, allowedProviderIP(netip.MustParseAddr(raw)), raw)
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		require.True(t, allowedProviderIP(netip.MustParseAddr(raw)), raw)
	}
}

func TestMatureProtocolProviderCredentialsRejectHeaderInjection(t *testing.T) {
	_, err := encodeProviderToken("token\r\nX-Injected: 1", "", "Bearer")
	require.Error(t, err)
	_, err = encodeProviderToken("", "re\nfresh", "Bearer")
	require.Error(t, err)
	_, err = encodeProviderToken("plain-token", "", "Bearer")
	require.NoError(t, err)
}
