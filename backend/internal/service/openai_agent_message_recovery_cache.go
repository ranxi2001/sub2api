package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// OpenAIAgentRecoveryCache stores only scoped item/route hashes. Production
// Redis implements it independently of ordinary mutable scheduler stickiness.
// Records survive application restarts/nodes while Redis retains them. Missing
// or expired records cannot establish compatibility for unknown old history.
type OpenAIAgentRecoveryCache interface {
	GetOpenAIAgentRecovery(ctx context.Context, key string) (string, error)
	PublishOpenAIAgentRecovery(ctx context.Context, key, route string, ttl time.Duration) error
}

func encodeOpenAIAgentRecoveryBinding(b openAIAgentRecoveryBinding) string {
	value, _ := json.Marshal([]any{1, b.accountID, hex.EncodeToString(b.config[:]), hex.EncodeToString(b.wire[:])})
	return string(value)
}

func decodeOpenAIAgentRecoveryBinding(raw string) (openAIAgentRecoveryBinding, bool, error) {
	b := openAIAgentRecoveryBinding{expiresAt: time.Now().Add(time.Minute)}
	if raw == "" {
		return b, false, nil
	}
	if raw == "failed" {
		b.failed = true
		return b, true, nil
	}
	if pending, route, ok := decodeOpenAIAgentRecoveryPending(raw); ok {
		b, found, err := decodeOpenAIAgentRecoveryBinding(route)
		b.pending = pending
		return b, found, err
	}
	if raw == "conflict" {
		b.conflicted = true
		return b, true, nil
	}
	var record []json.RawMessage
	var version int
	var config, wire string
	invalid := errors.New("invalid encrypted agent ownership record")
	if json.Unmarshal([]byte(raw), &record) != nil || len(record) != 4 ||
		json.Unmarshal(record[0], &version) != nil || version != 1 ||
		json.Unmarshal(record[1], &b.accountID) != nil || b.accountID <= 0 ||
		json.Unmarshal(record[2], &config) != nil || json.Unmarshal(record[3], &wire) != nil {
		return b, false, invalid
	}
	configBytes, err := hex.DecodeString(config)
	if err != nil || len(configBytes) != 32 {
		return b, false, invalid
	}
	wireBytes, err := hex.DecodeString(wire)
	if err != nil || len(wireBytes) != 32 {
		return b, false, invalid
	}
	copy(b.config[:], configBytes)
	copy(b.wire[:], wireBytes)
	return b, true, nil
}
