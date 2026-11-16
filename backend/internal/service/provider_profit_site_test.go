package service

import "testing"

func TestProviderProfitSiteKey(t *testing.T) {
	for _, raw := range []string{"https://alpha.invalid", "https://ALPHA.invalid.:443/", "https://alpha.invalid/v1/", "https://alpha.invalid/v1"} {
		if key := ProviderProfitSiteKey(raw); key != "https://alpha.invalid" {
			t.Errorf("key(%q) = %q", raw, key)
		}
	}
	for _, raw := range []string{"https://alpha.invalid:8443", "https://[2606:4700::1111]"} {
		if key := ProviderProfitSiteKey(raw + "/v1"); key != raw {
			t.Errorf("key(%q) = %q", raw, key)
		}
	}
	for _, raw := range []string{"", "unknown:multiple_effective_bases", "https://@alpha.invalid", "https://alpha.invalid/v2", "https://alpha.invalid/tenant/v1", "https://alpha.invalid/%76%31", "https://alpha.invalid/v1%2F", "https://alpha.invalid/?", "https://alpha.invalid?q=fixture", "https://alpha.invalid/#", "https://name:fixture@alpha.invalid", "http://alpha.invalid", "https://alpha.invalid:", "https://alpha.invalid:99999", "https://alpha.invalid../", "https://127.0.0.1", "https://localhost.", "https://[::1]", "https://[alpha.invalid]", "https://alpha.invalid//other"} {
		if key := ProviderProfitSiteKey(raw); key != "" {
			t.Errorf("unexpected key for %q: %q", raw, key)
		}
	}
}
