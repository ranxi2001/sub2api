package service

import (
	"context"
	"go.uber.org/zap"
	"reflect"
	"slices"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// Read ticket availability only: probe state never controls scheduling.
func (s *AccountTestService) startAstraAccountScheduling() func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			cycle, stop := context.WithTimeout(ctx, 5*time.Second)
			s.syncAstraAccountScheduling(cycle)
			stop()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

func astraAccountSchedulingReady(settings config.AstraRoutingSettings, snapshot AstraGatewayRuntime, id int64, now time.Time) bool {
	if !settings.CookiePool.Enabled || snapshot.Revision != settings.Revision {
		return false
	}
	if !slices.Contains(settings.CookiePool.TargetAccountIDs, id) {
		return false
	}
	if snapshot.RouteExpiresAt != nil && now.Before(*snapshot.RouteExpiresAt) {
		return true
	}
	for _, row := range snapshot.Targets {
		if row.AccountID == id && row.State == "ready" && row.ExpiresAt != nil && now.Before(*row.ExpiresAt) {
			return true
		}
	}
	return false
}

func (s *AccountTestService) syncAstraAccountScheduling(ctx context.Context) {
	settings := s.cfg.AstraRouting(ctx)
	if !settings.TicketPoolScheduling || s.openaiGatewayService == nil || !s.openaiGatewayService.openAICodexTicketEnabledContext(ctx) {
		return
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return
	}
	var ids []int64
	for i := range accounts {
		if s.openaiGatewayService.ticketPoolSchedulingIncludes(ctx, &accounts[i]) {
			ids = append(ids, accounts[i].ID)
		}
	}
	// Serialize writes with local saves; stale work cannot act on removed targets.
	if s.settingService != nil {
		s.settingService.astraRoutingMu.Lock()
		defer s.settingService.astraRoutingMu.Unlock()
		if current := s.settingService.astraRoutingCache; current == nil || !reflect.DeepEqual(*current, settings) {
			return
		}
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		account, err := s.accountRepo.GetByID(ctx, id)
		if err != nil || account == nil {
			continue
		}
		if !s.openaiGatewayService.ticketPoolSchedulingIncludes(ctx, account) {
			continue
		}
		ready := s.openaiGatewayService.ticketPoolSchedulingReady(account, settings)
		ready = ready && account.IsOpenAIOAuthLike() && account.Status == StatusActive && account.ParentAccountID == nil && (account.ExpiresAt == nil || time.Now().Before(*account.ExpiresAt))
		reason := "ticket_unavailable"
		if ready {
			reason = "ticket_ready"
		}
		fields := []zap.Field{zap.Int64("account_id", id), zap.Bool("schedulable", ready), zap.String("reason", reason), zap.String("trigger", "ticket_pool_sync")}
		action, err := applyAstraScheduling(ctx, s.accountRepo, account, settings, ready)
		if err != nil {
			code := "write_failed"
			switch err.Error() {
			case "astra_restore_conflict", "configuration_changed", "astra_model_mapping_invalid":
				code = err.Error()
			}
			logger.L().Warn("astra_account_scheduling_update_failed", append(fields, zap.String("action_error", code), zap.String("mode", settings.EffectiveSchedulingMode()))...)
			continue
		}
		if !action.Changed {
			continue
		}
		fields = append(fields, zap.String("mode", action.Action))
		logger.L().Info("astra_account_scheduling_changed", fields...)
		astraRecentScheduling.add(AstraSchedulingRecord{CheckedAt: time.Now(), AccountID: id, Schedulable: ready, Reason: reason, Mode: action.Action})
	}
}

// Only explicit non-secret fields: no account credentials, cookies, proxy URLs
// or upstream errors. unchanged states never call this logger.
func astraSchedulingLogFields(settings config.AstraRoutingSettings, snapshot AstraGatewayRuntime, account *Account, ready bool, now time.Time) []zap.Field {
	reason := "source_route_unavailable"
	gateway := ""
	expiry := snapshot.RouteExpiresAt
	for _, row := range snapshot.Targets {
		if row.AccountID == account.ID {
			if expiry == nil {
				expiry = row.ExpiresAt
			}
			if row.Gateway != "" {
				gateway = row.Gateway
			}
			if row.Reason != "" {
				reason = row.Reason
			}
			break
		}
	}
	for _, row := range snapshot.Sources {
		if row.Active {
			gateway = row.Gateway
			break
		}
	}
	if ready {
		if reason == "source_route_unavailable" {
			reason = "source_route_available"
		}
	} else if !settings.CookiePool.Enabled {
		reason = "cookie_pool_disabled"
	} else if snapshot.Revision != settings.Revision {
		reason = "configuration_changed"
	} else if !account.IsOpenAIOAuthLike() || account.Status != StatusActive || account.ParentAccountID != nil || (account.ExpiresAt != nil && !now.Before(*account.ExpiresAt)) {
		reason = "account_ineligible"
	} else if expiry != nil && !now.Before(*expiry) {
		reason = "route_expired"
	}
	remaining := int64(0)
	fields := []zap.Field{zap.Int64("account_id", account.ID), zap.Bool("previous_schedulable", account.Schedulable), zap.Bool("schedulable", ready), zap.String("reason", reason), zap.String("gateway", gateway), zap.String("revision", settings.Revision), zap.String("trigger", "astra_state_sync")}
	if expiry != nil {
		fields = append(fields, zap.Time("route_expires_at", *expiry))
		if now.Before(*expiry) {
			remaining = int64(expiry.Sub(now).Seconds())
		}
	}
	return append(fields, zap.Int64("route_remaining_seconds", remaining))
}

// Both acquisition paths publish into the same per-account ticket store.
// Scheduling only reads admission state; it never probes or borrows a ticket.
func (s *OpenAIGatewayService) ticketPoolSchedulingIncludes(ctx context.Context, account *Account) bool {
	if s == nil || account == nil || !isOpenAICodexTicketAccount(account) || openAICodexSkipHarvest(account) {
		return false
	}
	scope, err := s.settingService.GetCodexTicketHarvestScope(ctx)
	return err == nil && scope.includes(account)
}
func (s *OpenAIGatewayService) ticketPoolSchedulingReady(account *Account, settings config.AstraRoutingSettings) bool {
	cfg := s.openAICodexTicketConfig()
	for _, model := range cfg.Models {
		model = normalizeOpenAICodexTicketModel(model)
		if model == "" || (settings.EffectiveSchedulingMode() == "model" && model != "gpt-6-astra") {
			continue
		}
		if !isOpenAICodexTicketAccount(account, model) {
			continue
		}
		if s.lookupOpenAICodexTicket(account, model).valid(time.Now(), openAICodexTicketTargetLength(account, cfg)) {
			return true
		}
	}
	return false
}
