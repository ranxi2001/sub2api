package service

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

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
				if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
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
