package service

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

var ErrCodexTicketResponseRejected = errors.New("ticket response rejected; upstream may have charged; request was not replayed")

func (s *OpenAIGatewayService) boundCodexTicket(req *http.Request, account *Account) *openAICodexTicket {
	if req == nil {
		return nil
	}
	return s.boundCodexTicketFromHeader(req.Context(), req.Header, account)
}

func (s *OpenAIGatewayService) boundCodexTicketFromHeader(ctx context.Context, h http.Header, account *Account) *openAICodexTicket {
	if account != nil && (account.AuthConnectionID != "" || account.HasManagedOpenAIConnections()) {
		return nil
	}
	if s == nil || h == nil || account == nil || !s.openAICodexTicketEnabledContext(ctx) {
		return nil
	}
	sent := h.Get(openAICodexTurnStateHeader)
	if sent == "" {
		return nil
	}
	for _, model := range s.openAICodexTicketConfig().Models {
		ticket := s.lookupOpenAICodexTicket(account, model)
		if ticket != nil && ticket.State == sent && !ticket.Revoked {
			return ticket
		}
	}
	return nil
}

func (s *OpenAIGatewayService) codexTicketRequestBound(req *http.Request, account *Account) bool {
	return s.boundCodexTicket(req, account) != nil
}

func openAICodexReturnedStateAcceptable(account *Account, returned string, now time.Time, targetLen int) (openAICodexTicketShape, bool) {
	returned = strings.TrimSpace(returned)
	shape, err := parseOpenAICodexTicketShape(returned)
	if err != nil || targetLen <= 0 || len(returned) != targetLen || !strings.HasPrefix(returned, openAICodexTicketStatePrefix) {
		return shape, false
	}
	if targetLen != 780 && shape.Blocks != openAICodexTicketExpectedBlocks(account) {
		return shape, false
	}
	if shape.IssuedAt.After(now.Add(30*time.Second)) || !now.Before(shape.IssuedAt.Add(codexTicketLifetime(targetLen))) {
		return shape, false
	}
	return shape, true
}

func codexTicketExpiryFromShape(ttlSeconds int, shape openAICodexTicketShape, now time.Time) time.Time {
	if ttlSeconds <= 0 {
		ttlSeconds = 3600
	}
	expires := now.Add(time.Duration(ttlSeconds) * time.Second)
	if issuedExpiry := shape.IssuedAt.Add(codexTicketLifetime(4 * ((57 + 16*shape.Blocks + 2) / 3))); !shape.IssuedAt.IsZero() && issuedExpiry.Before(expires) {
		expires = issuedExpiry
	}
	return expires
}

func codexResponseMismatches(req *http.Request, resp *http.Response, account *Account) bool {
	if req == nil || resp == nil || account == nil || resp.StatusCode != http.StatusOK || req.Header.Get(openAICodexTurnStateHeader) == "" {
		return false
	}
	returned := extractOpenAICodexTurnState(resp.Header)
	if returned == "" {
		return false
	}
	_, ok := openAICodexReturnedStateAcceptable(account, returned, time.Now(), len(req.Header.Get(openAICodexTurnStateHeader)))
	return !ok
}

// A mismatch invalidates only the ticket actually sent by this request. The
// current response continues through normal accounting; never replay it here.
// A valid returned 292 that differs from the harvest blob is a turn rotation:
// official Codex mints a new state each turn, and later requests must send it.
func (s *OpenAIGatewayService) observeCodexTicketResponse(req *http.Request, resp *http.Response, account *Account) {
	if s == nil || req == nil || resp == nil || account == nil || resp.StatusCode != http.StatusOK {
		return
	}
	sent := req.Header.Get(openAICodexTurnStateHeader)
	returned := extractOpenAICodexTurnState(resp.Header)
	routeChanged := false
	for _, c := range resp.Cookies() {
		if c.Name == "__cflb" || c.Name == "__oailb" {
			routeChanged = true
		}
	}
	if len(sent) == 780 && returned == "" && routeChanged {
		returned = sent
	}
	if sent == "" || returned == "" || !s.openAICodexTicketEnabledContext(req.Context()) {
		return
	}
	cfg := s.openAICodexTicketConfig()
	now := time.Now()
	targetLen := openAICodexTicketTargetLength(account, cfg)
	shape, acceptable := openAICodexReturnedStateAcceptable(account, returned, now, targetLen)
	returnedCookies := responseCookiePairs(resp)
	if acceptable && returned == sent && len(returnedCookies) == 0 && !routeChanged {
		return
	}
	s.openaiCodexTicketStateMu.Lock()
	defer s.openaiCodexTicketStateMu.Unlock()
	for _, model := range cfg.Models {
		current := s.lookupCodexTicketLocked(account, model)
		if current == nil || current.State != sent || current.Revoked {
			continue
		}
		next := *current
		if len(next.HarvestCookies) > 0 && next.HarvestCookiesAt.IsZero() {
			next.HarvestCookiesAt = current.CapturedAt
		}
		if current.Length == 780 {
			route := current.HarvestCookies
			changed := routeChanged
			if changed {
				route = returnedCookies
			}
			clean, _, err := codex780Route(route, current.Gateway, now)
			// Probe evidence belongs to the exact ticket and route pair. A
			// business response cannot transfer it to unverified replacements.
			acceptable = acceptable && err == nil && returned == sent && slices.Equal(clean, current.HarvestCookies)
			returnedCookies = clean
		}
		if acceptable {
			next.State = returned
			next.Length = len(returned)
			next.Blocks = shape.Blocks
			next.IssuedAt = shape.IssuedAt
			next.ExpiresAt = codexTicketExpiryFromShape(cfg.TTLSeconds, shape, now)
			next.Revoked = false
			next.CapturedAt = now
			if len(returnedCookies) > 0 {
				next.HarvestCookies = mergeCookiePairs(current.HarvestCookies, returnedCookies)
				next.HarvestCookiesAt = now
			}
		} else if current.Standby.valid(now, targetLen) {

			next = *current.Standby
		} else {
			next.Revoked = true
		}
		s.openaiCodexTickets.Store(openAICodexTicketKey(account.ID, model), &next)
		if s.accountRepo != nil {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), time.Second)
			_ = s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{openAICodexTicketExtraKey(model): &next})
			cancel()
		}
	}
}
