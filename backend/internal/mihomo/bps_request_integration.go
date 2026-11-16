//go:build bps_request_integration

package mihomo

import "context"

// InstallBPSRequestIntegrationProbe replaces only the external reachability
// probe in explicitly tagged local integration tests. The production build has
// no symbol or configuration that can bypass its verified HTTPS probe.
func InstallBPSRequestIntegrationProbe(probe func(context.Context, string) error) func() {
	previous := bpsStaticManager
	bpsStaticManager = &Manager{bpsStaticMode: true, bpsProbe: probe}
	return func() { bpsStaticManager = previous }
}
