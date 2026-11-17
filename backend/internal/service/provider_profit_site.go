package service

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

// ProfitOAuthDynamicKey is the operations identity for an account whose
// upstream address is chosen at request time. It has no stable host, so profit
// grouping must not invent one.
const ProfitOAuthDynamicKey = "unknown:oauth_dynamic_endpoint"

// ProfitURLMayClaim reports whether a unique site-key match may own this
// account. An explicit bind, including one to a different channel, and a
// resolved provider import both win. A manual unlink (no channel) falls back
// to the unique site. With no recorded decision, a competing import origin
// still blocks the claim.
func ProfitURLMayClaim(recordedAssignment, originHash, siteOrigin string) bool {
	switch recordedAssignment {
	case "manual_unlinked":
		return true
	case "":
		return originHash == "" || originHash == siteOrigin
	default:
		return false
	}
}

// ProfitOAuthMayClaim is true when an oauth/dynamic account has no channel yet
// and may use a provider import or provider_binding. It is never a host match.
func ProfitOAuthMayClaim(key, recordedAssignment string) bool {
	if key != ProfitOAuthDynamicKey {
		return false
	}
	switch recordedAssignment {
	case "", "manual_unlinked":
		return true
	default:
		return false
	}
}

// ProviderProfitSiteKey identifies a dashboard origin or its literal /v1 API
// base for profit accounting only. Never resolve a host, use this as a network
// destination, or drop a tenant path in order to make an account match.
func ProviderProfitSiteKey(raw string) string {
	if len(raw) > 2048 || strings.ContainsAny(raw, "?#%\\") {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || strings.HasSuffix(u.Host, ":") {
		return ""
	}
	switch u.EscapedPath() {
	case "", "/", "/v1", "/v1/":
	default:
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || urlvalidator.IsBlockedHost(host) {
		return ""
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Is6() {
		if !strings.HasPrefix(u.Host, "[") {
			return ""
		}
		host = "[" + host + "]"
	} else {
		if strings.HasPrefix(u.Host, "[") || len(host) > 253 {
			return ""
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return ""
			}
			for _, c := range label {
				if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
					return ""
				}
			}
		}
	}
	if port := u.Port(); port != "" && port != "443" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || port != strconv.Itoa(n) {
			return ""
		}
		host += ":" + port
	}
	return "https://" + host
}
