package service

import (
	"context"
	"time"
)

// OpenAIAgentRecoveryRetentionCache preserves an existing proven route while
// its owner waits for a quota reset. Refresh must compare the exact stored route
// atomically, never recreate missing evidence or change another producer's state,
// and never shorten existing retention. False means the evidence no longer matches.
type OpenAIAgentRecoveryRetentionCache interface {
	RefreshOpenAIAgentRecovery(ctx context.Context, key, route string, ttl time.Duration) (bool, error)
}
