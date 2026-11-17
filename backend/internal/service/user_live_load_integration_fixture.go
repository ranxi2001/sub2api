//go:build integration

package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

type userLiveLoadFixtureAttestation struct{}

func (userLiveLoadFixtureAttestation) Check(context.Context) error { return nil }
func (userLiveLoadFixtureAttestation) Generate(context.Context) (string, error) {
	return `{"v":1,"s":0,"t":"offline-fixture"}`, nil
}

// NewUserLiveLoadGatewayForTest only injects offline platform dependencies.
// CreateLiveCall, scheduler admission, handoff and terminal release stay real.
func NewUserLiveLoadGatewayForTest(cache GatewayCache, concurrency *ConcurrencyService, snapshot *SchedulerSnapshotService, upstream HTTPUpstream) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cache:                 cache,
		concurrencyService:    concurrency,
		schedulerSnapshot:     snapshot,
		httpUpstream:          upstream,
		liveAttestation:       userLiveLoadFixtureAttestation{},
		liveAttestationCipher: newLiveAttestationCipher(&config.Config{JWT: config.JWTConfig{Secret: "offline-live-load-fixture"}}),
	}
}

func UserLiveLoadKeyReservationForTest(keyID int64, requestID string) *APIKeySlotReservation {
	return &APIKeySlotReservation{
		apiKeyID: keyID, requestID: requestID, limit: 1,
		release: func() {}, pauseRenewal: func() {},
	}
}
