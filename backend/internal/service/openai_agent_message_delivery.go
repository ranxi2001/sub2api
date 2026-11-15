package service

import (
	"context"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Pending is an exposure fence, never proof that delivery or consumption worked.
// Failed fences retain the original affinity TTL so a raced consumer cannot
// downgrade a failed/ambiguous delivery to an ordinary first-seen route.
type OpenAIAgentRecoveryDeliveryCache interface {
	OpenAIAgentRecoveryCache
	ReserveOpenAIAgentRecovery(context.Context, string, string, string, time.Duration) error
	FinishOpenAIAgentRecovery(context.Context, string, string, string, bool, time.Duration) error
}

// Association epochs are delivery fences, separate from immutable owner
// routes and optional readable payload storage. Reuse revokes the old epoch
// before any output bytes are exposed, including when shadow writes fail.
type OpenAIAgentRecoveryEpochCache interface {
	ReserveOpenAIAgentRecoveryEpoch(context.Context, string, string, time.Duration) error
	FinishOpenAIAgentRecoveryEpoch(context.Context, string, string, bool, time.Duration) error
	GetOpenAIAgentRecoveryEpoch(context.Context, string) (string, error)
}

type openAIAgentRecoveryDelivery struct {
	service *OpenAIGatewayService
	ctx     context.Context
	request *openAIAgentRecoveryRequest
	binding openAIAgentRecoveryBinding
	pending string
	keys    map[[32]byte]bool
	epochs  map[string]bool
	once    sync.Once
}

func newOpenAIAgentRecoveryDelivery(s *OpenAIGatewayService, ctx context.Context, r *openAIAgentRecoveryRequest, b openAIAgentRecoveryBinding) *openAIAgentRecoveryDelivery {
	return &openAIAgentRecoveryDelivery{service: s, ctx: ctx, request: r, binding: b, pending: "pending:" + uuid.NewString() + ":" + encodeOpenAIAgentRecoveryBinding(b), keys: make(map[[32]byte]bool), epochs: make(map[string]bool)}
}

func (s *OpenAIGatewayService) awaitOpenAIAgentRecovery(ctx context.Context, shared OpenAIAgentRecoveryCache, state *openAIAgentRecoveryState, key [32]byte) (openAIAgentRecoveryBinding, bool, error) {
	observedPending := false
	for {
		var b openAIAgentRecoveryBinding
		var found bool
		var err error
		if shared != nil {
			var raw string
			raw, err = shared.GetOpenAIAgentRecovery(ctx, hex.EncodeToString(key[:]))
			if err == nil {
				b, found, err = decodeOpenAIAgentRecoveryBinding(raw)
			}
		} else {
			state.mu.Lock()
			b, found = state.bindings[key]
			state.mu.Unlock()
			found = found && time.Now().Before(b.expiresAt)
		}
		if err != nil {
			if observedPending && ctx.Err() != nil {
				return b, false, openAIAgentRecoveryError("delivery_pending")
			}
			return b, false, openAIAgentRecoveryError("store_unavailable")
		}
		if !found && !observedPending {
			return openAIAgentRecoveryBinding{}, false, nil
		}
		if b.conflicted {
			return b, found, nil
		}
		if b.failed || (!found && observedPending) {
			return b, false, openAIAgentRecoveryError("owner_unavailable")
		}
		if b.pending == "" {
			return b, found, nil
		}
		observedPending = true
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return b, false, openAIAgentRecoveryError("delivery_pending")
		case <-timer.C:
		}
	}
}

func (d *openAIAgentRecoveryDelivery) reserve(items [][32]byte) error {
	if len(items) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(d.ctx, 3*time.Second)
	defer cancel()
	for _, item := range items {
		key := d.request.itemKey(item)
		epochKey := d.request.shadowEpochKey(item)
		if shared, ok := d.service.cache.(OpenAIAgentRecoveryEpochCache); ok && d.request.userID > 0 && !d.epochs[epochKey] {
			d.epochs[epochKey] = true
			if err := shared.ReserveOpenAIAgentRecoveryEpoch(ctx, epochKey, d.pending, d.service.openAIWSSessionStickyTTL()); err != nil {
				return openAIAgentRecoveryError("store_unavailable")
			}
		}
		if (d.request.disableOwnerBinding || d.request.omitted400History) && d.request.known[key] {
			// An echoed completed input is still its original producer's item.
			// Never reserve/publish it as the account consuming this history.
			continue
		}
		if d.keys[key] {
			continue
		}
		// Track an ambiguous reserve too: the server may have applied it before a
		// network failure. Cleanup uses token CAS, so it cannot revoke another grant.
		d.keys[key] = true
		ttl := d.service.openAIWSSessionStickyTTL()
		route := encodeOpenAIAgentRecoveryBinding(d.binding)
		if shared, ok := d.service.cache.(OpenAIAgentRecoveryCache); ok {
			transactional, ok := shared.(OpenAIAgentRecoveryDeliveryCache)
			if !ok {
				return openAIAgentRecoveryError("store_unavailable")
			}
			if err := transactional.ReserveOpenAIAgentRecovery(ctx, hex.EncodeToString(key[:]), route, d.pending, ttl); err != nil {
				return openAIAgentRecoveryError("store_unavailable")
			}
		} else {
			state := &d.service.agentMessageRecovery
			state.mu.Lock()
			if state.bindings == nil {
				state.bindings = make(map[[32]byte]openAIAgentRecoveryBinding)
			}
			for k, b := range state.bindings {
				if !time.Now().Before(b.expiresAt) {
					delete(state.bindings, k)
				}
			}
			old, found := state.bindings[key]
			switch {
			case found && (old.conflicted || old.failed || old.pending != "" || !sameOpenAIAgentRecoveryRoute(old, d.binding)):
				old.conflicted = true
				state.bindings[key] = old
				state.mu.Unlock()
				return openAIAgentRecoveryError("owner_conflict")
			case found: // Existing completed compatibility survives a failed producer.
			case len(state.bindings) >= openAIAgentRecoveryMaxEntries:
				state.mu.Unlock()
				return openAIAgentRecoveryError("store_unavailable")
			default:
				b := d.binding
				b.pending = d.pending
				b.expiresAt = time.Now().Add(ttl)
				state.bindings[key] = b
			}
			state.mu.Unlock()
		}
	}
	return nil
}

func (d *openAIAgentRecoveryDelivery) finish(completed bool) {
	d.once.Do(func() {
		final := make(map[[32]byte]bool)
		if completed {
			for _, item := range d.request.outputs {
				final[d.request.itemKey(item)] = true
			}
		}
		// Canceled requests must still revoke their pending exposure fences.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(d.ctx), 3*time.Second)
		defer cancel()
		for key := range d.keys {
			commit := final[key]
			ttl := d.service.openAIWSSessionStickyTTL()
			if shared, ok := d.service.cache.(OpenAIAgentRecoveryDeliveryCache); ok {
				route := encodeOpenAIAgentRecoveryBinding(d.binding)
				if err := shared.FinishOpenAIAgentRecovery(ctx, hex.EncodeToString(key[:]), route, d.pending, commit, ttl); err != nil {
					// A failed/ambiguous commit leaves pending (or a completed CAS); it can
					// never create a miss which would authorize an unrelated route.
					logger.L().Warn("openai.agent_recovery_commit_failed", zap.Int64("account_id", d.binding.accountID), zap.Error(err))
				}
			} else if _, shared := d.service.cache.(OpenAIAgentRecoveryCache); !shared {
				state := &d.service.agentMessageRecovery
				state.mu.Lock()
				if b, found := state.bindings[key]; found && b.pending == d.pending && !b.conflicted {
					b.pending = ""
					b.failed = !commit
					b.expiresAt = time.Now().Add(ttl)
					state.bindings[key] = b
				}
				state.mu.Unlock()
			}
		}
		if completed {
			d.publishReadableShadow(ctx)
		}
		if shared, ok := d.service.cache.(OpenAIAgentRecoveryEpochCache); ok {
			eligible := make(map[string]bool)
			if completed {
				for _, item := range d.request.outputs {
					eligible[d.request.shadowEpochKey(item)] = true
				}
			}
			for key := range d.epochs {
				// Failed/unknown publication leaves no eligible record matching
				// this epoch. An ambiguous CAS cannot restore an older epoch.
				_ = shared.FinishOpenAIAgentRecoveryEpoch(ctx, key, d.pending, eligible[key], d.service.openAIWSSessionStickyTTL())
			}
		}
	})
}

func decodeOpenAIAgentRecoveryPending(raw string) (string, string, bool) {
	if !strings.HasPrefix(raw, "pending:") {
		return "", "", false
	}
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) != 3 || parts[1] == "" {
		return "", "", false
	}
	return raw, parts[2], true
}
