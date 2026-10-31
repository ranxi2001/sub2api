package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

// OpenAITurnAdmissionReader reads the account, its group membership and (for a
// shadow) its credential parent from ONE primary-database snapshot.
// It must never fall back to the scheduler's eventually consistent cache.
type OpenAITurnAdmissionReader interface {
	GetOpenAITurnAdmission(context.Context, int64) (*Account, *Account, error)
}

// OpenAITurnAdmissionError is a local, PRE-SEND rejection, not an upstream
// failure. In particular it must not update account/proxy health or cooldown.
type OpenAITurnAdmissionError struct {
	Reason             string
	cause              error
	initialHTTPForward bool
}

func (e *OpenAITurnAdmissionError) Error() string { return "request admission denied: " + e.Reason }
func (e *OpenAITurnAdmissionError) Unwrap() error { return e.cause }

func IsOpenAITurnAdmissionError(err error) bool {
	var denied *OpenAITurnAdmissionError
	return errors.As(err, &denied)
}

// markOpenAIInitialAdmissionError is used only at the entry to an HTTP forward,
// before any upstream work. Later per-send/WS admission failures must stay
// unmarked: an earlier send may already have executed the request.
func markOpenAIInitialAdmissionError(err error) error {
	var denied *OpenAITurnAdmissionError
	if !errors.As(err, &denied) || denied.Reason != "account_binding_changed" {
		return err
	}
	copy := *denied
	copy.initialHTTPForward = true
	return &copy
}

// IsOpenAIInitialAdmissionRejection identifies a route change before this HTTP
// forward started. Callers must still check cancellation, downstream writes,
// continuation state and their account-switch budget before reselecting.
func IsOpenAIInitialAdmissionRejection(err error) bool {
	var denied *OpenAITurnAdmissionError
	return errors.As(err, &denied) && denied.initialHTTPForward
}

// invalidateOpenAIWSTurnStateAfterAdmissionFailure removes only the sticky
// state that can route the current conversation back to an account which has
// just failed the authoritative pre-send check.  It deliberately does not
// touch account data, tickets, quotas, or unrelated sessions.
//
// A database/read failure is not evidence that the account became ineligible,
// so callers must not invoke this helper for latest_state_unavailable.  Keeping
// that distinction prevents a transient control-plane outage from destroying
// otherwise valid continuation state.
func (s *OpenAIGatewayService) invalidateOpenAIWSTurnStateAfterAdmissionFailure(
	ctx context.Context,
	groupID int64,
	sessionHash string,
	responseID string,
	accountID int64,
	err error,
) {
	var denied *OpenAITurnAdmissionError
	if !errors.As(err, &denied) || denied == nil || denied.Reason == "latest_state_unavailable" || errors.Is(err, ErrPriorityLatencyUnavailable) {
		return
	}

	sessionHash = strings.TrimSpace(sessionHash)
	responseID = strings.TrimSpace(responseID)
	stateStore := s.getOpenAIWSStateStore()
	if stateStore != nil {
		if responseID != "" {
			// Delete both routing and connection affinity for the exact response
			// that was rejected.  Redis deletion errors are intentionally not
			// promoted to a request failure: the authoritative admission check
			// already prevented the send, and the next scheduler read will
			// revalidate the account.
			_ = stateStore.DeleteResponseAccount(ctx, groupID, responseID)
			stateStore.DeleteResponseConn(responseID)
		}
		if sessionHash != "" {
			stateStore.DeleteSessionTurnState(groupID, sessionHash)
			stateStore.DeleteSessionConn(groupID, sessionHash)
		}
	}

	if groupID != 0 && sessionHash != "" {
		group := groupID
		_ = s.deleteStickySessionAccountID(ctx, &group, sessionHash)
	}

	logOpenAIWSModeInfo(
		"openai_ws_admission_state_invalidated account_id=%d group_id=%d session_hash=%s response_binding_removed=%v reason=%s",
		accountID,
		groupID,
		truncateOpenAIWSLogValue(sessionHash, 12),
		responseID != "",
		normalizeOpenAIWSLogValue(denied.Reason),
	)
}

// invalidateOpenAIWSTurnStateAfterAdmissionFailureForRequest derives the
// request-scoped state keys used by all WS ingress variants.  Hooks may wrap a
// turn-admission error in a client-close error before it reaches the transport
// adapter, so the cleanup helper must accept the wrapped error as well.
func (s *OpenAIGatewayService) invalidateOpenAIWSTurnStateAfterAdmissionFailureForRequest(
	ctx context.Context,
	c *gin.Context,
	payload []byte,
	accountID int64,
	err error,
) {
	if !IsOpenAITurnAdmissionError(err) {
		return
	}

	groupID := getOpenAIGroupIDFromContext(c)
	sessionHash := ""
	if c != nil {
		sessionHash = strings.TrimSpace(c.GetString(openAIWSIngressSessionHashContextKey))
	}
	if sessionHash == "" && len(payload) > 0 {
		sessionHash = s.GenerateSessionHash(c, payload)
		if scope, _ := resolveOpenAIWSExecutionScope(c, payload, getAPIKeyIDFromContext(c)); scope != "" {
			sessionHash = scope
		}
	}
	responseID := openAIWSPayloadStringFromRaw(payload, "previous_response_id")
	s.invalidateOpenAIWSTurnStateAfterAdmissionFailure(
		ctx,
		groupID,
		sessionHash,
		responseID,
		accountID,
		err,
	)
}

// AdmitOpenAITurn performs the authoritative, pre-send eligibility check for
// one OpenAI turn.  Keep this as the exported boundary used by handlers: the
// actual predicate must stay in this service so HTTP, WS, bridge and
// passthrough paths cannot gradually drift apart.
func (s *OpenAIGatewayService) AdmitOpenAITurn(
	ctx context.Context,
	c *gin.Context,
	selected *Account,
	outboundModel string,
) (*Account, error) {
	return s.admitOpenAITurn(ctx, c, selected, outboundModel)
}

func denyOpenAITurn(reason string) error { return &OpenAITurnAdmissionError{Reason: reason} }

// IsOpenAIModelCapabilityRejection distinguishes a known unsupported model
// from other admission failures (credentials, capacity, tickets, or DB reads).
func IsOpenAIModelCapabilityRejection(err error) bool {
	var denied *OpenAITurnAdmissionError
	return errors.As(err, &denied) && (denied.Reason == "model_not_supported" || denied.Reason == "image_model_not_supported")
}

func denyOpenAITicket() error {
	return &OpenAITurnAdmissionError{Reason: "model_ticket_unavailable", cause: ErrOpenAICodexTicketUnavailable}
}

// Used only to detect a binding change, never logged. Include routing/identity
// settings but not mutable authentication credentials, usage, or
// harvested-ticket observations.
//
// Authentication credentials intentionally do not participate in this
// fingerprint. OAuth refresh and agent-task renewal replace access
// credentials during the normal lifetime of an account without changing its
// routing identity. Non-secret credential settings such as base_url and
// protocol mappings remain part of the fingerprint because they do affect
// where or how the request is sent.
func openAITurnRouteFingerprint(a *Account) [32]byte {
	if a == nil {
		return [32]byte{}
	}
	routeExtra := make(map[string]any)
	for _, key := range []string{
		codexFingerprintSeedExtraKey, codexFingerprintModeExtraKey,
		OpenAISIWCEnabledKey, "openai_passthrough", "openai_oauth_passthrough", "openai_excel_bps", "openai_excel_bps_models", "openai_excel_bps_mihomo",
		"openai_prism_browser", PrismBrowserModelsKey,
		"openai_oauth_responses_websockets_v2_mode", "openai_apikey_responses_websockets_v2_mode",
		"openai_oauth_responses_websockets_v2_enabled", "openai_apikey_responses_websockets_v2_enabled",
		"responses_websockets_v2_enabled", "openai_ws_enabled", "openai_ws_force_http",
		"openai_compact_mode", "openai_compact_supported",
		"openai_responses_mode", "openai_responses_supported",
		"openai_responses_flatten_namespaces", "enable_tls_fingerprint", "tls_fingerprint_profile_id",
		"codex_cli_only", "codex_cli_only_allow_app_server",
	} {
		if value, ok := a.Extra[key]; ok {
			routeExtra[key] = value
		}
	}
	proxyURL := ""
	if a.Proxy != nil {
		proxyURL = a.Proxy.URL()
	}
	routeCredentials := make(map[string]any)
	for key, value := range a.Credentials {
		switch key {
		case "access_token", "refresh_token", "id_token", "_token_version",
			"expires_at", "expires_in", "token_type", "scope":
			continue
		}
		if IsSensitiveCredentialKey(key) || strings.HasPrefix(key, "_token_") {
			continue
		}
		routeCredentials[key] = value
	}
	b, _ := json.Marshal(struct {
		Platform, Type string
		Parent, Proxy  *int64
		Credentials    map[string]any
		RouteExtra     map[string]any
		ProxyURL       string
	}{a.Platform, a.Type, a.ParentAccountID, a.ProxyID, routeCredentials, routeExtra, proxyURL})
	return sha256.Sum256(b)
}

func openAITurnAdmissionGroupFromContext(c *gin.Context) (int64, bool) {
	if c == nil || getAPIKeyFromContext(c) == nil {
		return 0, false
	}
	return getOpenAIGroupIDFromContext(c), true
}

func (s *OpenAIGatewayService) latestOpenAITurnAccount(ctx context.Context, c *gin.Context, selected *Account) (*Account, error) {
	groupID, enforceGroup := openAITurnAdmissionGroupFromContext(c)
	return s.latestOpenAITurnAccountForGroup(ctx, selected, groupID, enforceGroup)
}

func (s *OpenAIGatewayService) latestOpenAITurnAccountForGroup(
	ctx context.Context,
	selected *Account,
	groupID int64,
	enforceGroup bool,
) (*Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if selected == nil {
		return nil, denyOpenAITurn("account_unavailable")
	}
	if !selected.IsOpenAI() && (s.schedulerSnapshot == nil || s.schedulerSnapshot.localSnapshotReader == nil) {
		// This patch changes OpenAI Responses admission only, not other
		// providers which share the generic forwarding implementation.
		return selected, nil
	}
	latest := selected
	var parent *Account
	authoritativeRead := false
	if s.schedulerSnapshot != nil && s.schedulerSnapshot.localSnapshotReader != nil {
		// A regional reader is authoritative for every pre-send recheck. Its TTL
		// and persistence fence must never be bypassed by the process admission
		// cache, including already established WebSocket turns.
		var err error
		latest, err = s.schedulerSnapshot.GetAccount(ctx, selected.ID)
		if err != nil || latest == nil || latest.ID != selected.ID || latest.IsShadow() {
			return nil, denyOpenAITurn("latest_state_unavailable")
		}
		authoritativeRead = true
	} else {
		if ttl := s.openAITurnAdmissionCacheTTL(); ttl > 0 && ctx.Value(priorityLifecyclePrimaryReadKey{}) != true {
			if cached, cachedParent, ok := s.openAITurnAdmissionCache.load(selected.ID, ttl); ok {
				latest, parent, authoritativeRead = cached, cachedParent, true
			}
		}
		if s.accountRepo != nil {
			reader, ok := s.accountRepo.(OpenAITurnAdmissionReader)
			if !ok {
				if s.requireLatestTurnAdmission {
					return nil, denyOpenAITurn("latest_state_unavailable")
				}
				// Small direct-call fixtures may provide a repository for
				// credential-parent resolution without implementing the combined
				// authoritative admission reader. The production constructor
				// enables requireLatestTurnAdmission and therefore remains
				// fail-closed.
				reader = nil
			}
			if reader != nil && !authoritativeRead {
				authoritativeRead = true
				readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				var err error
				latest, parent, err = reader.GetOpenAITurnAdmission(readCtx, selected.ID)
				cancel()
				if err != nil || latest == nil || latest.ID != selected.ID {
					return nil, denyOpenAITurn("latest_state_unavailable")
				}
				if s.openAITurnAdmissionCacheTTL() > 0 {
					s.openAITurnAdmissionCache.store(latest, parent)
				}
			}
		} else if s.requireLatestTurnAdmission {
			// Real services are constructed with this flag. Small unit-test services
			// can exercise the pure supplied-account predicate without a database.
			return nil, denyOpenAITurn("latest_state_unavailable")
		}
	}
	if latest.Platform != selected.Platform || (latest.Type != selected.Type && selected.AuthConnectionID == "") {
		return nil, denyOpenAITurn("account_binding_changed")
	}
	if authoritativeRead && !latest.IsSchedulable() {
		return nil, denyOpenAITurn("account_ineligible")
	}
	if authoritativeRead && latest.IsShadow() && (parent == nil || parent.IsShadow() ||
		!parent.IsOpenAIOAuth() || !parent.IsCredentialUsableForShadow()) {
		return nil, denyOpenAITurn("credential_parent_ineligible")
	}
	// Simple mode deliberately schedules across the whole platform and does
	// not bind an account to the API key's group. Keep the final pre-send
	// admission predicate aligned with the scheduler instead of rejecting an
	// account that the scheduler just selected.
	enforceGroup = enforceGroup && (s == nil || s.cfg == nil || s.cfg.RunMode != config.RunModeSimple)
	if enforceGroup {
		if (groupID != 0 && !slices.Contains(latest.GroupIDs, groupID)) ||
			(groupID == 0 && len(latest.GroupIDs) != 0) {
			return nil, denyOpenAITurn("group_membership_changed")
		}
	}
	// A new local block installed before its DB write must not be cleared by an
	// older snapshot. Unlike the scheduler fast path, this is a pure read.
	if raw, ok := s.openaiAccountRuntimeBlockUntil.Load(latest.ID); ok {
		if until, valid := raw.(time.Time); valid && time.Now().Before(until) {
			return nil, denyOpenAITurn("account_runtime_blocked")
		}
	}
	return latest, nil
}

// Check every actual send, including retries and skipBeforeTurn paths. This is
// an admission point, not an atomic transaction spanning SQL and network I/O.
func (s *OpenAIGatewayService) admitOpenAITurn(ctx context.Context, c *gin.Context, selected *Account, outboundModel string, payload ...[]byte) (*Account, error) {
	if len(payload) > 0 {
		ctx = WithOpenAIImageGenerationRequest(ctx, outboundModel, payload[0])
	}
	groupID, enforceGroup := openAITurnAdmissionGroupFromContext(c)
	latest, err := s.admitOpenAITurnWithGroup(ctx, selected, outboundModel, groupID, enforceGroup)
	if err != nil {
		return nil, err
	}
	return s.prepareAuthConnection(ctx, c, latest, selected, outboundModel)
}

// admitOpenAITurnForGroup is used by connection-pool callbacks, which run
// after the request's gin context has been reduced to a plain context.  The
// group scope is captured up front with openAITurnAdmissionGroupFromContext,
// so a stale account cannot still complete a new handshake after it has been
// removed from the API key's group, while keyless account tests stay unscoped
// exactly as on the HTTP path.
func (s *OpenAIGatewayService) admitOpenAITurnForGroup(
	ctx context.Context,
	groupID int64,
	enforceGroup bool,
	selected *Account,
	outboundModel string,
	payload ...[]byte,
) (*Account, error) {
	if len(payload) > 0 {
		ctx = WithOpenAIImageGenerationRequest(ctx, outboundModel, payload[0])
	}
	return s.admitOpenAITurnWithGroup(ctx, selected, outboundModel, groupID, enforceGroup)
}

func (s *OpenAIGatewayService) admitOpenAITurnWithGroup(
	ctx context.Context,
	selected *Account,
	outboundModel string,
	groupID int64,
	enforceGroup bool,
) (*Account, error) {
	latest, err := s.latestOpenAITurnAccountForGroup(ctx, selected, groupID, enforceGroup)
	if err != nil {
		return nil, err
	}
	if err := s.validateExcelOAuthRoute(ctx, latest, outboundModel); err != nil && !latest.HasManagedOpenAIConnections() {
		return nil, &OpenAITurnAdmissionError{Reason: excelOAuthRouteErrorCode, cause: err}
	}
	// 会话中途被收窄了分组内的可用模型、或后续 turn 换成了不允许的模型时，要求客户端重连重新选号。
	if enforceGroup && !latest.IsModelAllowedInGroup(&groupID, outboundModel) {
		return nil, denyOpenAITurn("model_not_allowed_in_group")
	}
	if !latest.IsOpenAI() {
		return latest, nil
	}
	if openAITurnRouteFingerprint(latest) != openAITurnRouteFingerprint(selected) && (!latest.HasManagedOpenAIConnections() || selected.AuthConnectionID == "") {
		return nil, denyOpenAITurn("account_binding_changed")
	}
	// Preserve the cause of a model-not-found cooldown before the generic
	// runtime/rate-limit checks erase it.
	if latest.qualityModelBlocked(ctx, outboundModel) {
		return nil, denyOpenAITurn("model_runtime_blocked")
	}
	if latest.modelCapabilityRejected(outboundModel) {
		return nil, denyOpenAITurn("model_not_supported")
	}
	if reason := openAIImageRequestRejection(ctx, latest, outboundModel); reason != "" {
		return nil, denyOpenAITurn(reason)
	}
	if s.getOpenAIAccountModelTransientState().isBlocked(latest.ID, openAIAccountModelTransientModel(outboundModel), time.Now()) {
		return nil, denyOpenAITurn("model_runtime_blocked")
	}
	if latest.IsOpenAI() && (latest.isRateLimitActiveForKey(outboundModel) ||
		(openAIImageGenerationRateLimitApplies(ctx, outboundModel, outboundModel) &&
			latest.isRateLimitActiveForKey(openAIImageGenerationRateLimitKey))) {
		return nil, denyOpenAITurn("model_rate_limited")
	}
	if !latest.HasManagedOpenAIConnections() && s.openAICodexTicketBlocksAccount(latest, outboundModel) {
		return nil, denyOpenAITicket()
	}
	return latest, nil
}

// A connection is bound to the ticket actually sent at handshake, not the
// response's turn-state header and not the most recently harvested standby.
type openAIWSTurnBinding struct {
	model       string
	ticket      *openAICodexTicket
	fingerprint [32]byte
	createdAt   time.Time
}

func (s *OpenAIGatewayService) bindOpenAIWSHandshake(account *Account, model string, headers http.Header) *openAIWSTurnBinding {
	b := &openAIWSTurnBinding{model: strings.TrimSpace(model), fingerprint: openAITurnRouteFingerprint(account), createdAt: time.Now()}
	ticket := s.lookupOpenAICodexTicket(account, b.model)
	if ticket != nil && ticket.State == headers.Get(openAICodexTurnStateHeader) {
		copy := *ticket
		copy.Standby = nil
		b.ticket = &copy
	}
	return b
}

func (s *OpenAIGatewayService) checkOpenAIWSBinding(account *Account, model string, b *openAIWSTurnBinding) error {
	if account.isPrismBrowserUpstreamModelEnabled(model) {
		return denyOpenAITurn("prism_requires_http")
	}
	if account.isExcelBPSUpstreamModelEnabled(model) {
		return denyOpenAITurn("excel_bps_requires_http")
	}
	if b == nil || openAITurnRouteFingerprint(account) != b.fingerprint ||
		time.Since(b.createdAt) >= openAIWSConnMaxAge {
		return denyOpenAITurn("connection_binding_expired")
	}
	if !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabled() ||
		!s.openAICodexTicketConfig().FailClosed || !s.openAICodexTicketGatedModel(model) {
		return nil
	}
	if b.ticket == nil {
		return denyOpenAITurn("connection_ticket_missing")
	}
	if b.model != strings.TrimSpace(model) || !b.ticket.valid(time.Now(), openAICodexTicketTargetLength(account, s.openAICodexTicketConfig())) ||
		!ticketIdentityMatches(account, b.ticket) {
		return denyOpenAITurn("connection_ticket_expired")
	}
	// Require the bound ticket still to be known to the authoritative inventory.
	// A new standby does not retire an otherwise valid primary binding.
	current := s.lookupOpenAICodexTicket(account, model)
	if current == nil || current.Revoked ||
		(current.State != b.ticket.State && (current.Standby == nil || current.Standby.Revoked || current.Standby.State != b.ticket.State)) {
		return denyOpenAITurn("connection_ticket_revoked")
	}
	return nil
}
