package service

import "github.com/Wei-Shaw/sub2api/internal/config"

// Keep historical regression overlays with an older Config buildable. An
// absent policy has the same behavior as an unconfigured production process.
func openAILegacyCompatibilityEnabled(cfg *config.Config) bool {
	policy, ok := any(cfg).(interface{ OpenAILegacyCompatibilityEnabled() bool })
	return ok && policy.OpenAILegacyCompatibilityEnabled()
}

func (s *OpenAIGatewayService) openAILegacyCompatibility() bool {
	return s != nil && openAILegacyCompatibilityEnabled(s.cfg)
}
