package qqbot

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var botBlockedPrefixes = func() []netip.Prefix {
	var prefixes []netip.Prefix
	for _, raw := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:10::/28", "2001:20::/28", "2001:db8::/32", "2002::/16"} {
		prefixes = append(prefixes, netip.MustParsePrefix(raw))
	}
	return prefixes
}()

type guardedBotTransport struct {
	s        *Service
	public   *http.Transport
	internal *http.Transport
}

func newBotHTTPClient(s *Service) *http.Client {
	public := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 120 * time.Second, MaxIdleConns: 16, IdleConnTimeout: 60 * time.Second, DialContext: botPublicDial}
	internal := public.Clone()
	internal.DialContext = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	return &http.Client{Timeout: 210 * time.Second, CheckRedirect: noRedirect, Transport: &guardedBotTransport{s: s, public: public, internal: internal}}
}

func botPublicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("bot destination DNS unavailable")
	}
	for _, ip := range ips {
		if !publicIP(ip.IP) {
			return nil, errors.New("bot destination is not public")
		}
	}
	// Dial the checked address, never re-resolve the hostname. TLS still uses
	// the original hostname for certificate validation.
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

func (t *guardedBotTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.User != nil || r.URL.Fragment != "" {
		return nil, errors.New("invalid bot destination")
	}
	if t.s.managedSiteBase != "" && r.URL.Scheme+"://"+r.URL.Host == t.s.managedSiteBase && strings.HasPrefix(r.URL.Path, "/v1/") {
		c, err := t.s.Config(r.Context())
		if err != nil {
			return nil, err
		}
		auth := r.Header.Get("Authorization")
		chat := c.AIConnection == "site" && c.SiteAPIKeyID > 0 && c.SiteOwnerID > 0 && c.APIKey != "" && auth == "Bearer "+c.APIKey
		image := c.ImageAIConnection == "site" && c.ImageSiteAPIKeyID > 0 && c.ImageSiteOwnerID > 0 && c.ImageAPIKey != "" && auth == "Bearer "+c.ImageAPIKey
		if chat || image {
			return t.internal.RoundTrip(r)
		}
		return nil, errors.New("managed bot connection identity mismatch")
	}
	if r.URL.Scheme != "https" {
		return nil, errors.New("external bot destination requires public HTTPS")
	}
	return t.public.RoundTrip(r)
}

// Only server wiring supplies this origin; it is never derived from a user
// supplied URL. The transport additionally verifies a managed key on each call.
func (s *Service) SetManagedSiteOrigin(base string) {
	u, err := url.Parse(base)
	if err == nil && u.Scheme == "http" && u.Hostname() == "127.0.0.1" && u.User == nil {
		s.managedSiteBase = u.Scheme + "://" + u.Host
	}
}

func (t *guardedBotTransport) CloseIdleConnections() {
	t.public.CloseIdleConnections()
	t.internal.CloseIdleConnections()
}
