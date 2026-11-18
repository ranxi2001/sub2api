package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// Singleflight owns the entire node hunt, not each individual HTTP attempt.
func (s *OpenAIGatewayService) probeOnceOpenAICodexTicket(ctx context.Context, account *Account, model string) {
	if account != nil && (account.AuthConnectionID != "" || account.HasManagedOpenAIConnections()) {
		return
	}
	if s == nil || !isOpenAICodexTicketAccount(account, model) || s.httpUpstream == nil {
		return
	}
	key := openAICodexTicketKey(account.ID, model)
	_, _, _ = s.openaiCodexTicketFlight.Do(key, func() (any, error) {
		s.huntCodexHarvestTicket(ctx, account, model)
		return nil, nil
	})
}

func (s *OpenAIGatewayService) huntCodexHarvestTicket(ctx context.Context, account *Account, model string) {
	if !s.codexHarvestRunMu.TryRLock() {
		return
	}
	defer s.codexHarvestRunMu.RUnlock()

	controls, _ := s.harvestControls(ctx)
	round, _ := ctx.Value(codexHarvestRoundKey{}).(*codexHarvestRound)
	if round == nil {
		round = &codexHarvestRound{limit: controls.Speed.MaxRequestsPerRound}
	}
	// Try one already collected donor before spending the same round budget
	// on node discovery. Manual collection remains the explicit self path.
	if fresh, ok := s.freshHarvestAccount(ctx, account, model); ok {
		attempted, reused := s.tryBorrowStoredAstraTicket(ctx, fresh, model, time.Duration(controls.Speed.AttemptTimeoutSeconds)*time.Second, func() bool {
			return s.reserveHarvestRequest(ctx, fresh, model, round, false)
		})
		if attempted && (reused.Kind == "account_error" || reused.Kind == "rate_limited") {
			round.stopped.Store(account.ID, struct{}{})
		}
		if attempted && (reused.Kind == "success" || reused.Terminal || reused.Kind == "account_error" || reused.Kind == "rate_limited" || reused.Kind == "persist_failed") {
			if reused.Kind != "success" {
				cooldown := astraTicketCooldown(controls.Speed.CooldownSeconds, reused.RetryAfter)
				s.openaiCodexTicketProbeCooldown.Store(openAICodexTicketKey(account.ID, model), time.Now().Add(cooldown))
			}
			return
		}
	}
	tried := map[string]bool{}
	attempts := 0
	var last codexHarvestProbeResult
	defer func() {
		if s.codexHarvest != nil {
			s.codexHarvest.setRuntime(func(r *CodexHarvestRuntime) { r.CurrentNode = "" })
		}
	}()
	for n := 0; ; n++ {
		current, configured := s.harvestControls(ctx)
		controls = current
		pool := s.openAICodexTicketHarvestIPPoolEnabled(ctx)
		maxAttempts := harvestMaxNodeAttempts(controls, pool)
		if n >= maxAttempts || round.AccountStopped(account.ID) || round.Used() >= min(round.limit, controls.Speed.MaxRequestsPerRound) || ctx.Err() != nil {
			break
		}
		fresh, ok := s.freshHarvestAccount(ctx, account, model)
		if !ok {
			return
		}
		account = fresh
		var poolExit harvestIPPoolExit
		proxy := ""
		if pool {
			if poolExit, ok = s.pickHarvestIPPoolExitFor(ctx, account, model, tried); !ok {
				break
			}
			proxy = poolExit.url
		} else if proxy = s.openAICodexTicketHarvestProxyURLContext(ctx); proxy == "" {
			return
		}
		token, _, err := s.GetAccessToken(ctx, account)
		if err != nil || strings.TrimSpace(token) == "" {
			last.Kind = "token_error"
			break
		}
		if !s.waitHarvestPace(ctx, controls, configured) {
			return
		}
		controls, _ = s.harvestControls(ctx)
		fresh, ok = s.freshHarvestAccount(ctx, account, model)
		if !ok {
			return
		}
		account = fresh
		maxAttempts = harvestMaxNodeAttempts(controls, pool)
		if n >= maxAttempts || round.AccountStopped(account.ID) || round.Used() >= min(round.limit, controls.Speed.MaxRequestsPerRound) {
			break
		}
		var attempt codexHarvestAttempt
		if pool {
			// Pool exits are plain proxies: no sidecar, no node learning, and
			// the ticket binds the concrete URL so requests reuse this exit.
			tried[proxy] = true
			attempt = codexHarvestAttempt{proxy: proxy, release: func() {}}
			nodeName := harvestIPPoolNodeName(poolExit)

			if s.codexHarvest != nil {
				s.codexHarvest.setRuntime(func(r *CodexHarvestRuntime) { r.CurrentNode = nodeName })
			}
		} else {
			if attempt, ok = s.prepareHarvestAttempt(ctx, account, model, proxy, tried, controls); !ok {
				break
			}
		}
		started := time.Now()
		session := s.harvestAttemptSession(account, model, attempt)
		result := s.executeCodexHarvestProbe(ctx, account, token, model, attempt.proxy, time.Duration(controls.Speed.AttemptTimeoutSeconds)*time.Second, func() bool {
			return s.reserveHarvestRequest(ctx, account, model, round, attempt.sidecar != nil)
		}, session)
		if result.Kind == "account_error" || result.Kind == "rate_limited" {
			round.stopped.Store(account.ID, struct{}{})
		}
		s.finishHarvestAttempt(ctx, attempt, result, time.Since(started), controls)
		if !result.Sent || result.Kind == "cancelled" {
			return
		}
		attempts++
		last = result
		cfg := s.openAICodexTicketConfig()

		if result.Kind == "success" {
			s.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(account.ID, model))
			ticket := codexHarvestTicket(account, model, result, cfg, attempts)
			bindCodexHarvestEgress(ticket, attempt, session)
			if err := s.storeOpenAICodexTicket(ctx, account, ticket); err != nil && s.codexHarvest != nil {
				s.codexHarvest.degrade("ticket persisted in memory only; database write failed")
			}
			s.recordCodexProbe(ctx, account, model, result.Kind, result.Status)
			logger.L().Info("openai_codex_ticket harvested", zap.Int64("account_id", account.ID), zap.String("model", model), zap.Int("attempts", attempts))
			return
		}
		if result.Terminal || result.Kind == "account_error" || result.Kind == "rate_limited" || (attempt.sidecar == nil && !pool) {
			break
		}
		if s.codexHarvest != nil {
			s.codexHarvest.setRuntime(func(r *CodexHarvestRuntime) { r.SelectionReason = "switch_after_" + result.Kind })
		}
	}
	if ctx.Err() != nil || last.Kind == "" {
		return
	}
	cooldown := time.Duration(controls.Speed.CooldownSeconds) * time.Second
	if last.RetryAfter > cooldown {
		cooldown = last.RetryAfter
	}
	s.openaiCodexTicketProbeCooldown.Store(openAICodexTicketKey(account.ID, model), time.Now().Add(cooldown))
	s.recordCodexProbe(ctx, account, model, last.Kind, last.Status)
	logger.L().Info("openai_codex_ticket probe miss", zap.Int64("account_id", account.ID), zap.String("model", model), zap.String("reason", last.Kind), zap.Int("http", last.Status), zap.Int("attempts", attempts))
}

// Without node learning a hunt makes a single attempt; an IP pool still
// rotates to the next exit on failure, bounded by the same per-hunt budget.
func harvestMaxNodeAttempts(controls CodexHarvestControls, pool bool) int {
	if controls.NodeMemoryEnabled || pool {
		return max(1, controls.Speed.MaxNodeAttempts)
	}
	return 1
}

// Pool exits are shown by their IP-management name; the URL may carry
// credentials and must not reach the flow view.
func harvestIPPoolNodeName(exit harvestIPPoolExit) string {
	if exit.name != "" {
		return "IP池 · " + exit.name
	}
	return "IP池"
}
