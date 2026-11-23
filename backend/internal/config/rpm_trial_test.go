//go:build rpm_trial

package config

import (
	"context"
	"testing"
)

func TestRPMTrialRouting(t *testing.T) {
	cfg := &Config{}
	cfg.Gateway.CodexGatewayPin.Enabled = true
	cfg.Gateway.CodexWSAnchor.Enabled = true
	check := func(settings AstraRoutingSettings) {
		t.Helper()
		if settings.CookiePool.Enabled || settings.WSSession.Enabled {
			t.Fatal("shared routing must be disabled in the RPM trial")
		}
	}
	check(cfg.AstraRouting(context.Background()))
	saved := AstraRoutingSettings{CookiePool: cfg.Gateway.CodexGatewayPin, WSSession: cfg.Gateway.CodexWSAnchor, Revision: "saved"}
	cfg.SetAstraRoutingLoader(func(context.Context) AstraRoutingSettings { return saved })
	runtime := cfg.AstraRouting(context.Background())
	check(runtime)
	if runtime.Revision != saved.Revision {
		t.Fatal("lost settings revision")
	}
	if saved.Validate() == nil {
		t.Fatal("accepted shared routing settings")
	}
}
