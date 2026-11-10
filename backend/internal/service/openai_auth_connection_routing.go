package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
)

func (a *Account) HasManagedOpenAIConnections() bool {
	return a != nil && a.Platform == PlatformOpenAI && a.Extra["openai_connections_managed"] == true
}

func (c *OpenAIAuthConnection) Supports(capability string) bool {
	if c.Status != "active" || (c.RefreshState != "" && c.RefreshState != "idle") || c.CooldownUntil != nil && time.Now().Before(*c.CooldownUntil) {
		return false
	}
	switch c.Kind {
	case "codex", "bps", "siwc", "api_key":
	default:
		return false
	}
	if capability == "" {
		capability = "responses"
	}
	for _, v := range c.Capabilities {
		if v == capability {
			return true
		}
	}
	return false
}

func (a *Account) managedConnectionSupports(capability string) bool {
	raw, err := json.Marshal(a.Extra["openai_auth_connections"])
	if err != nil {
		return false
	}
	var connections []*OpenAIAuthConnection
	if json.Unmarshal(raw, &connections) != nil {
		return false
	}
	for _, c := range connections {
		if c != nil && c.Preferred {
			return c.Supports(capability)
		}
	}
	for _, c := range connections {
		if c != nil && c.Supports(capability) {
			return true
		}
	}
	return false
}

func connectionCapabilityForRequest(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return "responses"
	}
	path := strings.TrimRight(c.Request.URL.Path, "/")
	switch {
	case strings.HasSuffix(path, "/responses/compact"):
		return "responses_compact"
	case strings.HasSuffix(path, "/chat/completions"), strings.HasSuffix(path, "/messages"):
		return "chat_completions"
	case strings.HasSuffix(path, "/embeddings"):
		return "embeddings"
	case strings.HasSuffix(path, "/responses"):
		return "responses"
	default:
		return "unsupported"
	}
}

func (s *OpenAIGatewayService) prepareAuthConnection(ctx context.Context, c *gin.Context, account, selected *Account, model string) (*Account, error) {
	if c != nil {
		c.Set("openai_auth_connection_id", "")
	}
	managed := account.HasManagedOpenAIConnections()
	if account != nil && account.Platform == PlatformOpenAI && s.excelOAuthReauth != nil {
		if repo, ok := s.excelOAuthReauth.repo.(OpenAIAuthConnectionRepository); ok {
			current, err := repo.AuthConnectionsManaged(ctx, account.ID)
			if err != nil {
				return nil, err
			}
			managed = current
		}
	}
	if !managed {
		return account, nil
	}

	if account.IsCredentialShadow() {
		return nil, infraerrors.BadRequest("OPENAI_CONNECTION_SHADOW_UNSUPPORTED", "Managed connections require the parent account route")
	}
	if s.excelOAuthReauth == nil {
		return nil, errors.New("authorization connection service unavailable")
	}
	connections, err := s.excelOAuthReauth.listRuntimeAuthConnections(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	capability := connectionCapabilityForRequest(c)
	pinned := ""
	if selected != nil {
		pinned = selected.AuthConnectionID
	}
	var chosen *OpenAIAuthConnection
	if pinned == "" {
		for _, candidate := range connections {
			if candidate.Preferred && !candidate.Legacy {
				pinned = candidate.ID
				break
			}
		}
	}
	if pinned != "" && c != nil && c.GetString("openai_auth_connection_session_hash") != "" {
		r, repoErr := s.excelOAuthReauth.connectionRepository()
		if repoErr != nil {
			return nil, repoErr
		}
		pinned, err = r.ResolveAuthConnectionAffinity(ctx, account.ID, getAPIKeyIDFromContext(c), c.GetString("openai_auth_connection_session_hash"), pinned)
		if err != nil {
			return nil, err
		}
	}
	for _, candidate := range connections {
		if candidate.Legacy {
			continue
		}
		if pinned != "" && candidate.ID != pinned {
			continue
		}
		if candidate.Supports(capability) && (chosen == nil || candidate.Preferred) {
			chosen = candidate
		}
	}
	if chosen == nil {
		return nil, infraerrors.BadRequest("OPENAI_CONNECTION_NOT_CAPABLE", "No authorized connection supports this endpoint; select or authorize an appropriate connection")
	}
	if _, err = s.excelOAuthReauth.ConnectionAccessToken(ctx, account.ID, chosen.ID); err != nil {
		return nil, err
	}
	// Refresh may have rotated this record. Never project a stale token/client.
	chosen, err = s.excelOAuthReauth.authConnection(ctx, account.ID, chosen.ID)
	if err != nil {
		return nil, err
	}
	if !chosen.Supports(capability) {
		return nil, connectionChanged()
	}
	creds, err := s.excelOAuthReauth.readConnectionCredentials(chosen)
	if err != nil {
		return nil, err
	}
	out := *account
	out.AuthConnectionID = chosen.ID
	out.AuthConnectionRevision = chosen.Revision
	out.Credentials = shallowCopyMap(account.Credentials)
	// Keep model configuration, remove every legacy credential and registration
	// before applying the chosen set. No cross-client token composition.
	for key := range out.Credentials {
		if IsSensitiveCredentialKey(key) {
			delete(out.Credentials, key)
		}
	}
	for _, key := range []string{"access_token", "refresh_token", "id_token", "api_key", "client_id", "expires_at", "chatgpt_account_id", "chatgpt_user_id", "email", "auth_mode", "openai_auth_mode", "agent_private_key", "task_id"} {
		delete(out.Credentials, key)
	}
	for key, value := range creds {
		out.Credentials[key] = value
	}
	out.Extra = shallowCopyMap(account.Extra)
	for key := range out.Extra {
		if IsOpenAICodexTicketPrivateExtraKey(key) || strings.HasPrefix(key, "codex_harvest") || key == OpenAICodexSkipHarvestExtraKey {
			delete(out.Extra, key)
		}
	}

	out.Extra["openai_connections_managed"] = true
	out.Extra["openai_connection_kind"] = chosen.Kind
	out.Extra[OpenAISIWCEnabledKey] = chosen.Kind == "siwc"
	out.Extra["openai_excel_bps"] = chosen.Kind == "bps"
	delete(out.Extra, "openai_excel_bps_models")
	delete(out.Extra, ExcelBPSAuthorizationPendingKey)
	out.Extra["openai_prism_browser"] = false
	out.Extra["openai_copilot_sdk"] = false
	out.Extra["openai_responses_supported"] = true
	out.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModeOff
	out.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeOff
	out.Extra["openai_apikey_responses_websockets_v2_enabled"] = false
	for _, key := range []string{"base_url", "api_protocol", "auth_mode", "openai_auth_mode", "header_overrides", "user_agent"} {
		delete(out.Credentials, key)
	}
	out.Extra["openai_oauth_responses_websockets_v2_enabled"] = false
	if chosen.Kind == "api_key" {
		out.Type = AccountTypeAPIKey
		out.Credentials["base_url"] = "https://api.openai.com"
	} else {
		out.Type = AccountTypeOAuth
	}
	if s.openAICodexTicketBlocksAccount(&out, model) {
		return nil, denyOpenAITicket()
	}
	if c != nil {
		c.Set("openai_auth_connection_id", chosen.ID)
		c.Set(codexAccountIdentitySourceContextKey, &out)
	}
	return &out, nil
}

// Connection-only credential failures never mutate account-wide security. An
// explicit account/workspace ban still follows the existing account policy;
// 429 remains account-scoped because entitlement sharing is not independently
// proven for these registrations.
func (s *OpenAIOAuthReauthService) FailAuthConnection(ctx context.Context, account *Account, status int) error {
	lease, lockErr := s.lockAuthConnection(ctx, account.ID, account.AuthConnectionID)
	if lockErr != nil {
		return nil
	} // A live refresh may already be healing the rejected access token.
	defer lease.release()

	c, err := s.authConnection(ctx, account.ID, account.AuthConnectionID)
	if err != nil {
		return err
	}
	if err = lease.bind(c); err != nil {
		return err
	}
	if c.Revision != account.AuthConnectionRevision {
		return nil
	}
	if status == 401 && c.Kind != "api_key" {
		creds, readErr := s.readConnectionCredentials(c)
		if readErr != nil {
			return readErr
		}
		creds["expires_at"] = "0"
		if err = s.encryptConnectionCredentials(c, creds); err != nil {
			return err
		}
	} else if status == 401 {
		c.Status = "pending"
	} else {
		until := time.Now().Add(time.Minute)
		c.CooldownUntil = &until
	}
	r, err := s.connectionRepository()
	if err != nil {
		return err
	}
	_, err = r.CASAuthConnection(ctx, c, c.Revision)
	return err
}

func (s *OpenAIOAuthReauthService) recordConnectionUsage(ctx context.Context, accountID, apiKeyID int64, id, requestID string) {
	repo, ok := s.repo.(interface {
		RecordAuthConnectionUsage(context.Context, int64, int64, string, string) error
	})
	if !ok {
		return
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := repo.RecordAuthConnectionUsage(recordCtx, accountID, apiKeyID, id, requestID); err != nil {
		slog.Warn("openai_connection_usage_attribution_failed", "account_id", accountID, "connection_id", id)
	}
}

func (s *OpenAIOAuthReauthService) ValidateAuthConnectionSend(ctx context.Context, a *Account) error {
	if a == nil || a.AuthConnectionID == "" {
		return nil
	}
	c, err := s.authConnection(ctx, a.ID, a.AuthConnectionID)
	if err != nil {
		return err
	}
	if c.Revision != a.AuthConnectionRevision || c.Status != "active" || (c.RefreshState != "" && c.RefreshState != "idle") || len(c.Capabilities) == 0 || c.CooldownUntil != nil && time.Now().Before(*c.CooldownUntil) {
		return connectionChanged()
	}
	return nil
}

// A cached pre-selection account must not send legacy credentials after an
// administrator has activated managed routing on another replica.
func (s *OpenAIOAuthReauthService) validateAuthConnectionPolicy(ctx context.Context, a *Account) error {
	if a == nil || a.Platform != PlatformOpenAI {
		return nil
	}
	repo, ok := s.repo.(OpenAIAuthConnectionRepository)
	if !ok {
		return nil
	}
	managed, err := repo.AuthConnectionsManaged(ctx, a.ID)
	if err != nil {
		return err
	}
	if managed && a.AuthConnectionID == "" {
		return infraerrors.Conflict("OPENAI_CONNECTION_SELECTION_CHANGED", "Account authorization selection changed; retry with the current connection")
	}
	if !managed && a.AuthConnectionID != "" {
		return infraerrors.Conflict("OPENAI_CONNECTION_SELECTION_CHANGED", "Managed routing was disabled; start a new request")
	}
	return nil
}

func (s *OpenAIGatewayService) attributeRegionalConnectionResult(ctx context.Context, c *gin.Context, account *Account, result *OpenAIForwardResult) {
	if result == nil || !result.RegionalMetered || result.AuthConnectionID == "" || s.excelOAuthReauth == nil || account == nil {
		return
	}
	s.excelOAuthReauth.recordConnectionUsage(ctx, account.ID, getAPIKeyIDFromContext(c), result.AuthConnectionID, resolveUsageBillingRequestID(ctx, result.RequestID))
}
