package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type astraCandySkipObservationKey struct{}

// Observe after the business stream releases its exit. No verdict is returned
// to routing, ticket observation, account state or scheduling. Capacity pressure
// drops this optional beta observation instead of delaying a business request.
func (s *OpenAIGatewayService) observeScheduledAstraCandy(ctx context.Context, headers http.Header, account *Account, ticket *openAICodexTicket) {
	if skip, _ := ctx.Value(astraCandySkipObservationKey{}).(bool); skip {
		return
	}
	if s.settingService == nil || account == nil || ticket == nil || ticket.Model != OpenAICodexStateProbeDefaultModel || ticket.Length != 780 || ticket.Transport != "sse" || !ticket.valid(time.Now(), 780) || IsOpenAICodexStateProbeRequest(ctx) {
		return
	}
	copy := *ticket
	copy.Standby = nil
	copy.HarvestCookies = append([]string(nil), ticket.HarvestCookies...)
	encoded, _ := json.Marshal(copy)
	key := sha256.Sum256(encoded)
	now := time.Now()
	s.astraCandyMu.Lock()
	for k, expiry := range s.astraCandySeen {
		if !now.Before(expiry) {
			delete(s.astraCandySeen, k)
		}
	}
	if _, seen := s.astraCandySeen[key]; seen || s.astraCandyRunning >= 2 || len(s.astraCandySeen) >= 2048 {
		s.astraCandyMu.Unlock()
		return
	}
	if s.astraCandySeen == nil {
		s.astraCandySeen = make(map[[32]byte]time.Time)
	}
	s.astraCandySeen[key] = openAICodexTicketRemainingUntil(&copy)
	s.astraCandyRunning++
	s.astraCandyMu.Unlock()
	h := headers.Clone()
	id := account.ID
	// The stream's request context is commonly cancelled immediately after close.
	// Use a separate bounded lifetime, carrying no caller cancellation/deadline.
	go func() {
		defer func() { s.astraCandyMu.Lock(); s.astraCandyRunning--; s.astraCandyMu.Unlock() }()
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		if !s.settingService.CodexTicketCandyObservation(probeCtx) {
			s.astraCandyMu.Lock()
			delete(s.astraCandySeen, key)
			s.astraCandyMu.Unlock()
			return
		}
		s.runScheduledAstraCandy(probeCtx, h, id, &copy)
	}()
}

// Exactly one SSE request with the captured account/ticket/cookies and its own
// lease for the recorded exit. Never pass the result through ticket response
// observation, cooldown handling, harvest persistence or failover.
func (s *OpenAIGatewayService) runScheduledAstraCandy(ctx context.Context, headers http.Header, id int64, ticket *openAICodexTicket) string {
	if !s.settingService.CodexTicketCandyObservation(ctx) {
		return "candy_disabled"
	}
	if s.accountRepo == nil || s.httpUpstream == nil {
		return "candy_unavailable"
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil || !astraTicketAccountEligible(ctx, account) || !ticketIdentityMatches(account, ticket) || headers.Get("Authorization") == "" || headers.Get("ChatGPT-Account-ID") != account.GetCredential("chatgpt_account_id") || !s.openAICodexTicketEnabledContext(ctx) {
		return "candy_stale"
	}
	proxy, release, err := s.pinStoredCodexTicketEgress(ctx, ticket, openAIAccountProxyURL(account))
	if err != nil {
		release()
		return "candy_unavailable"
	}
	defer release()
	if !s.settingService.CodexTicketCandyObservation(ctx) {
		return "candy_disabled"
	}
	if !ticket.valid(time.Now(), 780) {
		return "candy_stale"
	}
	routeChanged := false
	shot, err := fireOpenAICodexPromptShotRequest(ctx, headers, ticket.Model, ticket.State, strings.Join(ticket.HarvestCookies, "; "), "只输出最终答案数字，不要解释。", CandyPrompt, func(req *http.Request) (*http.Response, error) {
		req = req.WithContext(WithHTTPUpstreamRedirectsDisabled(context.WithValue(req.Context(), boundAstraTicketContextKey{}, true)))
		restoreBoundCodexTicketHarvestIdentity(req.Header, ticket)
		pinBoundCodexTicketHarvestIdentityRequestBody(req, ticket)
		resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
		if resp != nil {
			expected := req.Cookies()
			for _, got := range resp.Cookies() {
				for _, want := range expected {
					if got.Name == want.Name && (got.Value != want.Value || got.MaxAge < 0 || (!got.Expires.IsZero() && !time.Now().Before(got.Expires))) {
						routeChanged = true
					}
				}
			}
		}
		return resp, err
	})
	switch {
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
		return "candy_timeout"
	case err != nil || shot.status != http.StatusOK || shot.streamErr != nil:
		return "candy_error"
	case routeChanged:
		return "candy_route_changed"
	case shot.state != "" && shot.state != ticket.State:
		return "candy_ticket_changed"
	case shot.model != ticket.Model:
		return "candy_model_mismatch"
	case !CandyAnswerCorrect(shot.answer):
		return "candy_incorrect"
	default:
		return "candy_passed"
	}
}

func astraCandyStreamAnswer(data []byte) string {
	for _, event := range openAICodexStateStreamEvents(data) {
		if gjson.GetBytes(event, "type").String() != "response.completed" {
			continue
		}
		var answer strings.Builder
		for _, item := range gjson.GetBytes(event, "response.output").Array() {
			if item.Get("type").String() != "message" || item.Get("role").String() != "assistant" {
				continue
			}
			for _, content := range item.Get("content").Array() {
				if content.Get("type").String() == "output_text" {
					_, _ = answer.WriteString(content.Get("text").String())
				}
			}
		}
		return answer.String()
	}
	return ""
}
