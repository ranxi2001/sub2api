package service

import (
	"testing"
	"time"
)

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

func TestProfitURLAndOAuthClaim(t *testing.T) {
	if !ProfitURLMayClaim("manual_unlinked", "other-origin", "site-origin") {
		t.Fatal("a unique site may claim after a manual unlink")
	}
	if !ProfitURLMayClaim("", "", "site-origin") || !ProfitURLMayClaim("", "site-origin", "site-origin") {
		t.Fatal("an unbound account with no competing origin may claim")
	}
	if ProfitURLMayClaim("", "other-origin", "site-origin") {
		t.Fatal("a different import origin blocks a URL claim")
	}
	if ProfitURLMayClaim("explicit", "", "site-origin") || ProfitURLMayClaim("provider_import", "", "site-origin") {
		t.Fatal("an explicit or imported channel wins over the site key")
	}
	if !ProfitOAuthMayClaim(ProfitOAuthDynamicKey, "") || !ProfitOAuthMayClaim(ProfitOAuthDynamicKey, "manual_unlinked") {
		t.Fatal("oauth/dynamic accounts use a provider binding when no channel is set")
	}
	if ProfitOAuthMayClaim(ProfitOAuthDynamicKey, "explicit") || ProfitOAuthMayClaim("https://alpha.invalid/tenant/v1", "") || ProfitOAuthMayClaim("unknown:invalid_url", "") {
		t.Fatal("oauth claim must not invent a host match or override an explicit bind")
	}
}

func TestProfitSnapshotDatesUseShanghaiDays(t *testing.T) {
	loc := ProfitConsumptionLocation()
	from := time.Date(2026, 11, 16, 0, 0, 0, 0, loc)
	until := time.Date(2026, 11, 17, 0, 0, 0, 0, loc)
	start, end, ok := ProfitSnapshotDates(from, until)
	if !ok || start != "2026-11-16" || end != "2026-11-16" {
		t.Fatalf("yesterday = %s %s ok=%v", start, end, ok)
	}
	start, end, ok = ProfitSnapshotDates(from, time.Date(2026, 11, 18, 0, 0, 0, 0, loc))
	if !ok || start != "2026-11-16" || end != "2026-11-17" {
		t.Fatalf("two days = %s %s ok=%v", start, end, ok)
	}
	partial := time.Date(2026, 11, 16, 10, 0, 0, 0, loc)
	start, end, ok = ProfitSnapshotDates(partial, partial.Add(5*time.Hour))
	if !ok || start != "2026-11-16" || end != "2026-11-16" {
		t.Fatalf("partial day still covers its calendar date: %s %s ok=%v", start, end, ok)
	}
	if _, _, ok = ProfitSnapshotDates(until, from); ok {
		t.Fatal("a reversed window is not a date range")
	}
}
