package service

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
)

// AstraTicketLifecycle joins gateway reuse to the account ticket store. It is
// installed at construction, before either background collection or HTTP starts.
// Ticket values are applied directly to request headers; public DTOs contain
// only readiness metadata, never credential or routing cookie values.
type AstraTicketLifecycle interface {
	UnifiedAstraTicketsEnabled(context.Context) bool
	PrepareStoredAstraSources(context.Context) error
	VerifyStoredAstraTarget(context.Context, int64) error
	ApplyStoredAstraTarget(context.Context, http.Header, int64, string) (string, func(), error)
	StoredAstraSnapshot(context.Context) AstraGatewayRuntime
}

type boundAstraTicketContextKey struct{}

// IsBoundAstraTicketRequest prevents a later transport wrapper from replacing
// one half of a verified credential bundle. This marker is never an HTTP header.
func IsBoundAstraTicketRequest(ctx context.Context) bool {
	bound, _ := ctx.Value(boundAstraTicketContextKey{}).(bool)
	return bound
}

func (s *OpenAIGatewayService) UnifiedAstraTicketsEnabled(ctx context.Context) bool {
	return s != nil && s.cfg != nil && s.accountRepo != nil &&
		s.openAICodexTicketEnabledContext(ctx) && s.openAICodexTicketConfig().TargetLength == 780 &&
		s.openAICodexTicketGatedModel(OpenAICodexStateProbeDefaultModel) && s.cfg.AstraRouting(ctx).CookiePool.Enabled
}

func astraTicketAccountEligible(ctx context.Context, a *Account) bool {
	if a != nil && (a.AuthConnectionID != "" || a.HasManagedOpenAIConnections()) {
		return false
	}
	// Explicit preparation can restore a target paused by Astra scheduling, but
	// never bypasses authentication, expiry, transport or upstream cooldowns.
	if a == nil || !a.IsActive() || !isOpenAICodexTicketAccount(a, OpenAICodexStateProbeDefaultModel) || a.ParentAccountID != nil {
		return false
	}
	now := time.Now()
	return (a.ExpiresAt == nil || now.Before(*a.ExpiresAt)) && !a.IsRateLimited() && !a.IsOverloaded() &&
		(a.TempUnschedulableUntil == nil || !now.Before(*a.TempUnschedulableUntil)) &&
		!a.isModelRateLimitedWithContext(ctx, OpenAICodexStateProbeDefaultModel)
}

func (s *OpenAIGatewayService) storedAstraDonor(ctx context.Context, targetID int64) (*Account, *openAICodexTicket) {
	for _, id := range s.cfg.AstraRouting(ctx).CookiePool.SourceAccountIDs {
		if id == targetID || ctx.Err() != nil {
			continue
		}
		a, err := s.accountRepo.GetByID(ctx, id)
		if err != nil || !astraTicketAccountEligible(ctx, a) {
			continue
		}
		ticket := s.lookupOpenAICodexTicket(a, OpenAICodexStateProbeDefaultModel)
		if ticket != nil && ticket.Standby.valid(time.Now(), 780) && ticket.Standby.Transport == "sse" &&
			ticketIdentityMatches(a, ticket.Standby) && openAICodexTicketRemainingUntil(ticket.Standby).After(openAICodexTicketRemainingUntil(ticket)) {
			ticket = ticket.Standby
		}
		if ticket.valid(time.Now(), 780) && ticket.Transport == "sse" && time.Until(openAICodexTicketRemainingUntil(ticket)) > 30*time.Second {
			copy := *ticket
			copy.HarvestCookies = slices.Clone(ticket.HarvestCookies)
			return a, &copy
		}
	}
	return nil, nil
}

func (s *OpenAIGatewayService) PrepareStoredAstraSources(ctx context.Context) error {
	if !s.UnifiedAstraTicketsEnabled(ctx) {
		return errors.New("cookie_pool_disabled")
	}
	if _, ticket := s.storedAstraDonor(ctx, 0); ticket != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	controls, _ := s.harvestControls(ctx)
	ctx = context.WithValue(ctx, codexHarvestRoundKey{}, &codexHarvestRound{limit: controls.Speed.MaxRequestsPerRound})
	// Reuse is the fast path. When it has no donor, use the existing self
	// collector for source accounts rather than creating a second ticket store.
	for _, id := range s.cfg.AstraRouting(ctx).CookiePool.SourceAccountIDs {
		account, err := s.accountRepo.GetByID(ctx, id)
		if err != nil || !astraTicketAccountEligible(ctx, account) {
			continue
		}
		s.probeOnceOpenAICodexTicket(ctx, account, OpenAICodexStateProbeDefaultModel)
		if _, ticket := s.storedAstraDonor(ctx, 0); ticket != nil {
			return nil
		}
	}
	return errors.New("no_qualified_source_route")
}

// tryBorrowStoredAstraTicket runs at most one donor candidate. The caller owns
// the harvest lock and request budget; failure may fall back to self collection.
func (s *OpenAIGatewayService) tryBorrowStoredAstraTicket(ctx context.Context, account *Account, model string, timeout time.Duration, reserve func() bool) (bool, codexHarvestProbeResult) {
	var result codexHarvestProbeResult
	if !s.UnifiedAstraTicketsEnabled(ctx) || model != OpenAICodexStateProbeDefaultModel ||
		!slices.Contains(s.cfg.AstraRouting(ctx).CookiePool.TargetAccountIDs, account.ID) {
		return false, result
	}
	controls, configured := s.harvestControls(ctx)
	if controls.Transport != "" && controls.Transport != "sse" {
		return false, result // SSE admission never qualifies a WebSocket ticket.
	}
	settings := s.cfg.AstraRouting(ctx)
	donor, seed := s.storedAstraDonor(ctx, account.ID)
	if seed == nil {
		return false, result
	}
	if !s.waitHarvestPace(ctx, controls, configured) {
		result.Kind = "cancelled"
		return true, result
	}
	result.Kind = "account_error"
	if !astraTicketAccountEligible(ctx, account) || s.codexTicketChatHeld(account.ID) {
		return true, result
	}
	proxy, release, err := s.pinStoredCodexTicketEgress(ctx, seed, "")
	if err != nil || proxy == "" {
		release()
		result.Kind = "network_error"
		return true, result
	}
	defer release()
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil || token == "" {
		return true, result
	}
	identity := ticketIdentity(account)
	admitted := func() bool {
		if ctx.Err() != nil || !seed.valid(time.Now(), 780) || !s.UnifiedAstraTicketsEnabled(ctx) ||
			!config.AstraRouteSettingsEqual(settings, s.cfg.AstraRouting(ctx)) || s.codexTicketChatHeld(account.ID) {
			return false
		}
		fresh, e := s.accountRepo.GetByID(ctx, account.ID)
		return e == nil && astraTicketAccountEligible(ctx, fresh) && ticketIdentity(fresh) == identity && (reserve == nil || reserve())
	}
	session := uuid.NewString()
	ctx = context.WithValue(ctx, codex780SeedContextKey{}, codex780Seed{state: seed.State, cookies: seed.HarvestCookies})
	ctx = context.WithValue(ctx, codexMintEdgeContextKey{}, seed.EdgeIP)
	result = s.executeCodexHarvestProbe(ctx, account, token, model, proxy, timeout, admitted, session)

	if result.Kind != "success" {
		return true, result
	}
	fresh, err := s.accountRepo.GetByID(ctx, account.ID)
	if ctx.Err() != nil || err != nil || !astraTicketAccountEligible(ctx, fresh) || ticketIdentity(fresh) != identity ||
		!s.UnifiedAstraTicketsEnabled(ctx) || !config.AstraRouteSettingsEqual(settings, s.cfg.AstraRouting(ctx)) {
		result.Kind = "cancelled"
		return true, result
	}
	ticket := codexHarvestTicket(fresh, model, result, s.openAICodexTicketConfig(), 2)
	ticket.HarvestProxyURL, ticket.HarvestNodeID = seed.HarvestProxyURL, seed.HarvestNodeID
	ticket.HarvestNodeName, ticket.HarvestNodeProvider = seed.HarvestNodeName, seed.HarvestNodeProvider
	ticket.HarvestSessionID = session
	ticket.Acquisition, ticket.SourceAccountID = "reuse", donor.ID
	if err = s.storeOpenAICodexTicket(ctx, fresh, ticket); err != nil {
		// The legacy store retains memory on a database error. Reuse must not
		// publish a new ready target until the complete bundle is persisted.
		s.openaiCodexTicketStateMu.Lock()
		if current, ok := s.openaiCodexTickets.Load(openAICodexTicketKey(fresh.ID, model)); ok {
			if value, ok := current.(*openAICodexTicket); ok {
				copy := *value
				if copy.State == ticket.State {
					copy.Revoked = true
				} else if copy.Standby != nil && copy.Standby.State == ticket.State {
					copy.Standby = nil
				}
				s.openaiCodexTickets.Store(openAICodexTicketKey(fresh.ID, model), &copy)
			}
		}
		s.openaiCodexTicketStateMu.Unlock()
		result.Kind = "persist_failed"
		return true, result
	}
	s.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(fresh.ID, model))
	return true, result
}

func (s *OpenAIGatewayService) acquireStoredAstraTarget(ctx context.Context, id int64, protocol string) (*Account, *openAICodexTicket, error) {
	if !s.UnifiedAstraTicketsEnabled(ctx) || !slices.Contains(s.cfg.AstraRouting(ctx).CookiePool.TargetAccountIDs, id) {
		return nil, nil, errors.New("configuration_changed")
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil || !astraTicketAccountEligible(ctx, account) {
		return nil, nil, errors.New("account_unavailable")
	}
	if ticket := s.lookupOpenAICodexTicket(account, OpenAICodexStateProbeDefaultModel); ticket.valid(time.Now(), 780) && ticket.Transport == protocol {
		return account, ticket, nil
	}
	if protocol != "sse" {
		return nil, nil, ErrOpenAICodexTicketUnavailable
	}
	if _, ticket := s.storedAstraDonor(ctx, id); ticket == nil {
		if err := s.PrepareStoredAstraSources(ctx); err != nil {
			return nil, nil, err
		}
		account, err = s.accountRepo.GetByID(ctx, id)
		if err != nil || !astraTicketAccountEligible(ctx, account) {
			return nil, nil, errors.New("account_unavailable")
		}
		if ticket := s.lookupOpenAICodexTicket(account, OpenAICodexStateProbeDefaultModel); ticket.valid(time.Now(), 780) && ticket.Transport == protocol {
			return account, ticket, nil
		}
	}
	if !s.codexHarvestRunMu.TryLock() {
		return nil, nil, errors.New("target_validation_in_progress")
	}
	defer s.codexHarvestRunMu.Unlock()
	// A collector may have finished between the first read and acquisition.
	if ticket := s.lookupOpenAICodexTicket(account, OpenAICodexStateProbeDefaultModel); ticket.valid(time.Now(), 780) && ticket.Transport == protocol {
		return account, ticket, nil
	}
	if s.ticketProbeCoolingDown(id, OpenAICodexStateProbeDefaultModel, time.Now()) {
		return nil, nil, errors.New("source_probe_cooldown")
	}
	budget := 2
	controls, _ := s.harvestControls(ctx)
	timeout := time.Duration(controls.Speed.AttemptTimeoutSeconds) * time.Second
	if timeout < time.Second {
		timeout = 25 * time.Second
	}
	attempted, result := s.tryBorrowStoredAstraTicket(ctx, account, OpenAICodexStateProbeDefaultModel, timeout, func() bool {
		if budget <= 0 {
			return false
		}
		budget--
		return true
	})
	if !attempted {
		return nil, nil, errors.New("no_qualified_source_route")
	}
	if result.Kind != "success" {
		if ctx.Err() == nil {
			cooldown := astraTicketCooldown(controls.Speed.CooldownSeconds, result.RetryAfter)
			s.openaiCodexTicketProbeCooldown.Store(openAICodexTicketKey(id, OpenAICodexStateProbeDefaultModel), time.Now().Add(cooldown))
		}
		return nil, nil, errors.New("target_probe_failed")
	}
	ticket := s.lookupOpenAICodexTicket(account, OpenAICodexStateProbeDefaultModel)
	if !ticket.valid(time.Now(), 780) || ticket.Transport != protocol {
		return nil, nil, ErrOpenAICodexTicketUnavailable
	}
	return account, ticket, nil
}

func (s *OpenAIGatewayService) VerifyStoredAstraTarget(ctx context.Context, id int64) error {
	_, _, err := s.acquireStoredAstraTarget(ctx, id, "sse")
	return err
}

func (s *OpenAIGatewayService) ApplyStoredAstraTarget(ctx context.Context, h http.Header, id int64, protocol string) (string, func(), error) {
	// Forwarding only consumes an existing bundle. Acquisition and validation
	// belong to the collector/explicit diagnostics, never the caller's request.
	if !s.UnifiedAstraTicketsEnabled(ctx) || !slices.Contains(s.cfg.AstraRouting(ctx).CookiePool.TargetAccountIDs, id) {
		return "", func() {}, errors.New("configuration_changed")
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil || !astraTicketAccountEligible(ctx, account) {
		return "", func() {}, errors.New("account_unavailable")
	}
	ticket := s.lookupOpenAICodexTicket(account, OpenAICodexStateProbeDefaultModel)
	if !ticket.valid(time.Now(), 780) || ticket.Transport != protocol {
		return "", func() {}, ErrOpenAICodexTicketUnavailable
	}
	// Never combine another workspace with the caller's credentials. The
	// access-token provider may have refreshed a token since this DB snapshot.
	if h.Get("ChatGPT-Account-ID") != account.GetCredential("chatgpt_account_id") || h.Get("Authorization") == "" {
		return "", func() {}, errors.New("configuration_changed")
	}
	proxy, release, err := s.pinStoredCodexTicketEgress(ctx, ticket, "")
	if err != nil {
		return "", func() {}, err
	}
	h.Set(openAICodexTurnStateHeader, ticket.State)
	restoreBoundCodexTicketHarvestIdentity(h, ticket)
	releaseChat := s.holdCodexTicketChat(account)
	return proxy, func() { release(); releaseChat() }, nil
}

func (s *OpenAIGatewayService) StoredAstraSnapshot(ctx context.Context) AstraGatewayRuntime {
	settings := s.cfg.AstraRouting(ctx)
	now := time.Now()
	out := AstraGatewayRuntime{GeneratedAt: now, Revision: settings.Revision, Sources: []AstraRouteStatus{}, Targets: []AstraRouteStatus{}, WS: []AstraWSStatus{}}
	project := func(id int64, target bool) AstraRouteStatus {
		r := AstraRouteStatus{AccountID: id, State: "waiting", Reason: "target_not_verified"}
		account, err := s.accountRepo.GetByID(ctx, id)
		if err != nil || !astraTicketAccountEligible(ctx, account) {
			return r
		}
		ticket := s.lookupOpenAICodexTicket(account, OpenAICodexStateProbeDefaultModel)
		if !ticket.valid(now, 780) {
			return r
		}
		expires := openAICodexTicketRemainingUntil(ticket)
		r.State, r.Reason = "ready", "target_probe_passed"
		if !target {
			r.State, r.Reason = "candidate", "source_passed_pending_target"
		}
		r.CheckedAt, r.ExpiresAt = &ticket.CapturedAt, &expires
		r.RemainingSeconds = int64(expires.Sub(now).Seconds())
		r.Gateway = "chat.gateway." + ticket.Gateway + ".api.openai.com"
		r.ProxyNode = ticket.HarvestNodeName
		return r
	}
	for _, id := range settings.CookiePool.SourceAccountIDs {
		out.Sources = append(out.Sources, project(id, false))
	}
	for _, id := range settings.CookiePool.TargetAccountIDs {
		r := project(id, true)
		out.Targets = append(out.Targets, r)
		if r.State == "ready" {
			out.ReadyRoutes++
			if out.RouteExpiresAt == nil || (r.ExpiresAt != nil && r.ExpiresAt.Before(*out.RouteExpiresAt)) {
				out.RouteExpiresAt = r.ExpiresAt
			}
		}
	}
	return out
}

// Compile-time assertions keep the repository adapter independent of service internals.
var _ AstraTicketLifecycle = (*OpenAIGatewayService)(nil)

func astraTicketCooldown(seconds int, retry time.Duration) time.Duration {
	d := time.Duration(seconds) * time.Second
	if retry > d {
		d = retry
	}
	if d < time.Second {
		d = time.Second
	}
	return d
}
