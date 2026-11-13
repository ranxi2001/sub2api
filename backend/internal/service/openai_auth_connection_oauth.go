package service

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
)

type openAIConnectionTokenClient interface {
	ConnectionToken(context.Context, url.Values, string, string, string) (*openai.TokenResponse, *openai.SIWCIdentity, error)
	RevokeConnectionToken(context.Context, string, string, string) error
}

func (s *OpenAIOAuthReauthService) readConnectionCredentials(c *OpenAIAuthConnection) (map[string]any, error) {
	if c.CredentialsCiphertext == "" {
		return map[string]any{}, nil
	}
	plain, err := s.encryptor.Decrypt(c.CredentialsCiphertext)
	if err != nil {
		return nil, errors.New("failed to decrypt connection credentials")
	}
	var creds map[string]any
	if json.Unmarshal([]byte(plain), &creds) != nil || creds == nil {
		return nil, errors.New("invalid connection credentials")
	}
	if creds["_connection_id"] != c.ID || creds["_account_id"] != strconv.FormatInt(c.AccountID, 10) || creds["_connection_kind"] != c.Kind || creds["_connection_subject"] != c.Subject {
		return nil, errors.New("connection credential binding changed")
	}
	// The client/token association is authenticated by the encrypted record.
	if c.Kind != "api_key" && c.ClientID != "" && creds["client_id"] != c.ClientID {
		return nil, errors.New("connection client does not match its credentials")
	}
	return creds, nil
}
func (s *OpenAIOAuthReauthService) encryptConnectionCredentials(c *OpenAIAuthConnection, creds map[string]any) error {
	creds["_connection_id"] = c.ID
	creds["_account_id"] = strconv.FormatInt(c.AccountID, 10)
	creds["_connection_kind"] = c.Kind
	creds["_connection_subject"] = c.Subject

	raw, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	c.CredentialsCiphertext, err = s.encryptor.Encrypt(string(raw))
	if err != nil {
		return errors.New("failed to encrypt connection credentials")
	}
	return nil
}

func (s *OpenAIOAuthReauthService) BeginConnectionAuthorization(ctx context.Context, accountID int64, id string) (*OpenAIAuthURLResult, error) {
	if _, err := s.managementConnectionAccount(ctx, accountID); err != nil {
		return nil, err
	}
	c, err := s.authConnection(ctx, accountID, id)
	if err != nil {
		return nil, err
	}
	if c.Legacy {
		return nil, infraerrors.BadRequest("OPENAI_CONNECTION_LEGACY_READ_ONLY", "Use existing authorization controls for legacy connections")
	}
	if c.Kind == "api_key" {
		return nil, infraerrors.BadRequest("OPENAI_CONNECTION_NOT_OAUTH", "API keys do not use OAuth")
	}
	if err = s.ensureDurableEncryption(); err != nil {
		return nil, err
	}
	account, err := s.connectionAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	proxy, err := s.reauthProxyURL(ctx, account.ProxyID)
	if err != nil {
		return nil, err
	}
	state, err := openai.GenerateState()
	if err != nil {
		return nil, err
	}
	verifier, err := openai.GenerateCodeVerifier()
	if err != nil {
		return nil, err
	}
	nonce, err := openai.GenerateState()
	if err != nil {
		return nil, err
	}
	sessionID, err := openai.GenerateSessionID()
	if err != nil {
		return nil, err
	}
	clientID, redirect := c.ClientID, openai.DefaultRedirectURI
	switch c.Kind {
	case "codex":
		if clientID == "" {
			clientID = openai.ClientID
		}
		if clientID != openai.ClientID {
			return nil, errors.New("codex client registration mismatch")
		}
	case "bps":
		if clientID == "" {
			clientID = openai.ExcelClientID
		}
		if clientID != openai.ExcelClientID {
			return nil, errors.New("BPS client registration mismatch")
		}
		redirect = openai.ExcelRedirectURI
	case "siwc":
		redirect = openai.SIWCRedirectURI
	default:
		return nil, infraerrors.BadRequest("OPENAI_CONNECTION_KIND_INVALID", "Unknown connection kind")
	}
	session := &openai.OAuthSession{SIWCAccountID: accountID, ConnectionID: id, ConnectionRevision: c.Revision, ConnectionKind: c.Kind, SIWCNonce: nonce, SIWCHostID: c.HostID, State: state, CodeVerifier: verifier, ClientID: clientID, RedirectURI: redirect, ProxyURL: proxy, CreatedAt: time.Now()}
	if err = s.setSIWCSession(ctx, sessionID, session); err != nil {
		return nil, err
	}
	challenge := openai.GenerateCodeChallenge(verifier)
	var authURL string
	if c.Kind == "siwc" {
		// Do not embed retained ID tokens in a URL returned to the browser.
		authURL = openai.BuildSIWCAuthorizationURL(state, challenge, nonce, c.HostID, clientID, "", "")
	} else {
		authURL = openai.BuildAuthorizationURLForPlatform(state, challenge, redirect, PlatformOpenAI)
		if c.Kind == "bps" {
			authURL = openai.BuildExcelAuthorizationURL(state, challenge)
		}
		parsed, _ := url.Parse(authURL)
		q := parsed.Query()
		q.Set("nonce", nonce)
		parsed.RawQuery = q.Encode()
		authURL = parsed.String()
	}
	return &OpenAIAuthURLResult{SessionID: sessionID, AuthURL: authURL}, nil
}

func (s *OpenAIOAuthReauthService) CompleteConnectionAuthorization(ctx context.Context, accountID int64, id, sessionID, callback string) error {
	if _, err := s.managementConnectionAccount(ctx, accountID); err != nil {
		return err
	}
	c, err := s.authConnection(ctx, accountID, id)
	if err != nil {
		return err
	}
	session, ok := s.readSIWCSession(ctx, sessionID, false)
	if !ok || session.SIWCAccountID != accountID || session.ConnectionID != id || session.ConnectionKind != c.Kind || session.ConnectionRevision != c.Revision {
		return connectionChanged()
	}
	if len(callback) > maxOpenAIOAuthReauthURL {
		return infraerrors.BadRequest("OPENAI_CONNECTION_CALLBACK_INVALID", "Invalid callback")
	}
	u, err := url.Parse(callback)
	expected, _ := url.Parse(session.RedirectURI)
	if err != nil || u.Scheme != expected.Scheme || u.Host != expected.Host || u.Path != expected.Path || u.RawPath != "" || u.User != nil || u.Fragment != "" {
		return infraerrors.BadRequest("OPENAI_CONNECTION_CALLBACK_INVALID", "Callback does not match this connection")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return infraerrors.BadRequest("OPENAI_CONNECTION_CALLBACK_INVALID", "Invalid callback")
	}
	for _, key := range []string{"code", "state", "client_id", "error"} {
		if len(q[key]) > 1 {
			return infraerrors.BadRequest("OPENAI_CONNECTION_CALLBACK_INVALID", "Duplicate callback parameter")
		}
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(session.State)) != 1 {
		return infraerrors.BadRequest("OPENAI_CONNECTION_STATE_INVALID", "Invalid OAuth state")
	}
	if q.Get("error") != "" {
		s.deleteSIWCSession(ctx, sessionID)
		return infraerrors.BadRequest("OPENAI_CONNECTION_CONSENT_REQUIRED", "Authorization requires user consent")
	}
	clientID := session.ClientID
	if clientID == "" {
		clientID = q.Get("client_id")
	} else if q.Get("client_id") != "" && q.Get("client_id") != clientID {
		return infraerrors.BadRequest("OPENAI_CONNECTION_CLIENT_MISMATCH", "Connection client changed")
	}
	if clientID == "" || clientID == openai.SIWCDynamicClientID || q.Get("code") == "" {
		return infraerrors.BadRequest("OPENAI_CONNECTION_CALLBACK_INVALID", "Callback requires code and issued client")
	}
	lease, err := s.lockAuthConnection(ctx, accountID, id)
	if err != nil {
		return err
	}
	defer lease.release()
	operationCtx, stopOperation := context.WithTimeout(ctx, 50*time.Second)
	defer stopOperation()
	ctx = operationCtx
	current, err := s.authConnection(ctx, accountID, id)
	if err != nil {
		return err
	}
	if current.Revision != session.ConnectionRevision {
		return connectionChanged()
	}
	if err = lease.bind(current); err != nil {
		return err
	}
	c = current
	consumed, ok := s.readSIWCSession(ctx, sessionID, true)
	if !ok || consumed.ConnectionID != id || consumed.State != session.State {
		return connectionChanged()
	}
	client, ok := s.oauth.oauthClient.(openAIConnectionTokenClient)
	if !ok {
		return errors.New("connection OAuth adapter unavailable")
	}
	tokenCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	token, identity, err := client.ConnectionToken(tokenCtx, url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {q.Get("code")}, "code_verifier": {session.CodeVerifier}, "redirect_uri": {session.RedirectURI}}, session.ProxyURL, session.SIWCNonce, c.Kind)
	if err != nil {
		return err
	}
	if identity == nil || token == nil || token.AccessToken == "" || token.RefreshToken == "" {
		return errors.New("connection authorization is incomplete")
	}
	// Reauthorization may not switch subscriber or workspace. Email is never an
	// identity key and cannot prove equivalence across separate registrations.
	if c.Subject != "" && c.Subject != identity.Subject {
		return infraerrors.Conflict("OPENAI_CONNECTION_SUBJECT_CHANGED", "Reauthorization changed the connection identity")
	}
	if c.ClientID != "" && c.ClientID != clientID {
		return connectionChanged()
	}
	c.ClientID = clientID
	c.Subject = identity.Subject
	c.IdentityVerified = true
	c.Audience = clientID
	c.Issuer = "https://auth.openai.com"
	c.Status = "active"
	c.ResetRefresh = true
	c.CooldownUntil = nil
	c.Scopes = strings.Fields(token.Scope)
	c.Capabilities = connectionCapabilities(c.Kind, token.Scope)
	creds := map[string]any{"access_token": token.AccessToken, "refresh_token": token.RefreshToken, "id_token": token.IDToken, "client_id": clientID, "expires_at": strconv.FormatInt(time.Now().Unix()+token.ExpiresIn, 10)}
	if c.Kind != "siwc" {
		auth, parseErr := verifiedConnectionAuthMetadata(token.IDToken)
		if parseErr != nil {
			return parseErr
		}
		info := &openai.UserInfo{ChatGPTAccountID: auth.ChatGPTAccountID, ChatGPTUserID: auth.ChatGPTUserID, Email: identity.Email}
		if c.Workspace != "" && c.Workspace != info.ChatGPTAccountID {
			return infraerrors.Conflict("OPENAI_CONNECTION_WORKSPACE_CHANGED", "Reauthorization changed workspace")
		}
		c.Workspace = info.ChatGPTAccountID
		creds["chatgpt_account_id"] = info.ChatGPTAccountID
		creds["chatgpt_user_id"] = info.ChatGPTUserID
		creds["email"] = info.Email
	}
	creds[connectionAuthorizationReceipt] = connectionAuthorizationHash(sessionID)
	creds[connectionAuthorizationTime] = strconv.FormatInt(time.Now().Unix(), 10)
	if err = s.encryptConnectionCredentials(c, creds); err != nil {
		return err
	}
	r, err := s.connectionRepository()
	if err != nil {
		return err
	}
	persistCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	changed, err := r.CASAuthConnection(persistCtx, c, session.ConnectionRevision)
	if err != nil {
		return err
	}
	if !changed {
		return connectionChanged()
	}
	return nil
}

func connectionCapabilities(kind, scope string) []string {
	switch kind {
	case "siwc":
		if openai.HasSIWCPermission(scope) {
			return []string{"responses", "chat_completions"}
		}
	case "codex":
		return []string{"responses", "chat_completions", "responses_compact"}
	case "bps":
		return []string{"responses"}
	}
	return []string{}
}

func (s *OpenAIOAuthReauthService) ConnectionAccessToken(ctx context.Context, accountID int64, id string) (string, error) {
	c, err := s.authConnection(ctx, accountID, id)
	if err != nil {
		return "", err
	}
	if c.Legacy || c.Status != "active" || len(c.Capabilities) == 0 {
		return "", connectionRefreshUnavailable()
	}
	if c.RefreshState != "idle" && c.RefreshState != "" && c.RefreshCandidateCiphertext == "" {
		return "", connectionRefreshUnavailable()
	}
	creds, err := s.readConnectionCredentials(c)
	if err != nil {
		return "", err
	}
	if c.Kind == "api_key" {
		return stringCredential(creds, "api_key"), nil
	}
	expiry, _ := strconv.ParseInt(stringCredential(creds, "expires_at"), 10, 64)
	if c.RefreshCandidateCiphertext == "" && expiry > time.Now().Add(openAITokenRefreshSkew).Unix() {
		return stringCredential(creds, "access_token"), nil
	}
	lease, err := s.lockAuthConnection(ctx, accountID, id)
	if err != nil {
		return "", err
	}
	defer lease.release()
	leaseCtx, stop := context.WithTimeout(ctx, 50*time.Second)
	defer stop()
	ctx = leaseCtx
	c, err = s.authConnection(ctx, accountID, id)
	if err != nil {
		return "", err
	}
	if err = lease.bind(c); err != nil {
		return "", err
	}
	if c.Status != "active" {
		return "", connectionRefreshUnavailable()
	}
	r, err := s.connectionRepository()
	if err != nil {
		return "", err
	}
	if c.RefreshCandidateCiphertext != "" {
		return s.finishConnectionRefresh(ctx, r, c)
	}
	if c.RefreshState != "idle" {
		return "", connectionRefreshUnavailable()
	}
	creds, err = s.readConnectionCredentials(c)
	if err != nil {
		return "", err
	}
	expiry, _ = strconv.ParseInt(stringCredential(creds, "expires_at"), 10, 64)
	if expiry > time.Now().Add(openAITokenRefreshSkew).Unix() {
		return stringCredential(creds, "access_token"), nil
	}
	if !c.IdentityVerified || c.ClientID == "" || stringCredential(creds, "refresh_token") == "" {
		return "", connectionRefreshUnavailable()
	}
	account, err := s.connectionAccount(ctx, accountID)
	if err != nil {
		return "", err
	}
	proxy, err := s.reauthProxyURL(ctx, account.ProxyID)
	if err != nil {
		return "", err
	}
	client, ok := s.oauth.oauthClient.(openAIConnectionTokenClient)
	if !ok {
		return "", errors.New("connection OAuth adapter unavailable")
	}
	c.RefreshAttemptID = uuid.NewString()
	begun, err := r.BeginAuthConnectionRefresh(ctx, c, c.RefreshAttemptID)
	if err != nil {
		return "", err
	}
	if !begun {
		return "", connectionChanged()
	}
	defer func() {
		markCtx, cancelMark := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancelMark()
		_, _ = r.MarkAuthConnectionRefresh(markCtx, c, "uncertain")
	}()
	// Intent commits BEFORE a potentially rotating POST. Process death, timeout,
	// validation/encryption/storage failure all leave a durable closed gate.
	refreshCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	token, identity, err := client.ConnectionToken(refreshCtx, url.Values{"grant_type": {"refresh_token"}, "client_id": {c.ClientID}, "refresh_token": {stringCredential(creds, "refresh_token")}}, proxy, "", c.Kind)
	if err != nil {
		state := "uncertain"
		var outcome *OpenAIConnectionTokenError
		if errors.As(err, &outcome) {
			switch outcome.Outcome {
			case "not_sent":
				state = "idle"
			case "rejected":
				state = "rejected"
			}
		}
		persist, stopPersist := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stopPersist()
		_, _ = r.MarkAuthConnectionRefresh(persist, c, state)
		return "", err
	}
	if token == nil || token.AccessToken == "" || token.ExpiresIn <= 0 || identity != nil && identity.Subject != c.Subject {
		return "", connectionRefreshUnavailable()
	}
	if identity != nil && c.Kind != "siwc" && token.IDToken != "" {
		auth, e := verifiedConnectionAuthMetadata(token.IDToken)
		if e != nil || auth.ChatGPTAccountID != c.Workspace {
			return "", connectionRefreshUnavailable()
		}
	}
	if token.Scope != "" {
		c.Scopes = strings.Fields(token.Scope)
		c.Capabilities = connectionCapabilities(c.Kind, token.Scope)
	}
	creds["access_token"] = token.AccessToken
	creds["expires_at"] = strconv.FormatInt(time.Now().Unix()+token.ExpiresIn, 10)
	if token.RefreshToken != "" {
		creds["refresh_token"] = token.RefreshToken
	}
	if token.IDToken != "" {
		creds["id_token"] = token.IDToken
	}
	if err = s.encryptConnectionCredentials(c, creds); err != nil {
		return "", connectionRefreshUnavailable()
	}
	candidate, err := s.encryptRefreshCandidate(c)
	if err != nil {
		return "", connectionRefreshUnavailable()
	}
	persist, stopPersist := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stopPersist()
	staged, err := r.StageAuthConnectionRefresh(persist, c, candidate)
	if err != nil {
		return "", connectionRefreshUnavailable()
	}
	if !staged {
		return "", connectionChanged()
	}
	c.RefreshCandidateCiphertext = candidate
	return s.finishConnectionRefresh(persist, r, c)
}

func stringCredential(creds map[string]any, key string) string { v, _ := creds[key].(string); return v }

func (s *OpenAIOAuthReauthService) RevokeAuthConnection(ctx context.Context, accountID int64, id string, revision int64) (bool, error) {
	if _, err := s.managementConnectionAccount(ctx, accountID); err != nil {
		return false, err
	}
	lease, err := s.lockAuthConnection(ctx, accountID, id)
	if err != nil {
		return false, err
	}
	defer lease.release()
	operationCtx, stopOperation := context.WithTimeout(ctx, 50*time.Second)
	defer stopOperation()
	ctx = operationCtx
	c, err := s.authConnection(ctx, accountID, id)
	if err != nil {
		return false, err
	}
	if err = lease.bind(c); err != nil {
		return false, err
	}
	if c.Revision != revision {
		return false, connectionChanged()
	}
	creds, err := s.readConnectionCredentials(c)
	if err != nil {
		return false, err
	}
	// Stop locally first, fencing every in-flight rotation. Remote revocation
	// is best effort and is explicitly reported; it cannot revoke peer grants.
	c.Status = "unlinked"
	c.ResetRefresh = true
	c.CredentialsCiphertext = ""
	c.Capabilities = []string{}
	repo, err := s.connectionRepository()
	if err != nil {
		return false, err
	}
	changed, err := repo.CASAuthConnection(ctx, c, revision)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, connectionChanged()
	}
	if c.Kind == "api_key" {
		return false, nil
	} // Platform keys require manual revocation.
	account, err := s.connectionAccount(ctx, accountID)
	if err != nil {
		return false, nil
	}
	proxy, err := s.reauthProxyURL(ctx, account.ProxyID)
	if err != nil {
		return false, nil
	}
	client, ok := s.oauth.oauthClient.(openAIConnectionTokenClient)
	if !ok {
		return false, nil
	}
	revokeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return client.RevokeConnectionToken(revokeCtx, stringCredential(creds, "refresh_token"), c.ClientID, proxy) == nil, nil
}

type authConnectionLease struct {
	proof   OpenAIAuthConnectionLease
	release func()
}

func (l *authConnectionLease) bind(c *OpenAIAuthConnection) error {
	if l == nil || c == nil || c.LeaseOwner != l.proof.Owner || c.LeaseEpoch != l.proof.Epoch {
		return connectionChanged()
	}
	c.LeaseOwner = l.proof.Owner
	c.LeaseEpoch = l.proof.Epoch
	return nil
}
func (s *OpenAIOAuthReauthService) lockAuthConnection(ctx context.Context, accountID int64, id string) (*authConnectionLease, error) {
	r, err := s.connectionRepository()
	if err != nil {
		return nil, err
	}
	owner := uuid.NewString()
	proof, err := r.AcquireAuthConnectionLeaseProof(ctx, accountID, id, owner)
	if err != nil || proof == nil || proof.Owner != owner {
		return nil, infraerrors.Conflict("OPENAI_CONNECTION_REFRESH_BUSY", "Connection lifecycle operation is busy; retry later")
	}
	return &authConnectionLease{proof: *proof, release: func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = r.ReleaseAuthConnectionLease(cleanup, accountID, id, owner)
	}}, nil
}

// The adapter has already verified this exact ID token's signature, issuer,
// audience, nonce and expiry. Read only its provider metadata here; audience may
// legally be a string or array. A session ID is never a workspace identifier.
func verifiedConnectionAuthMetadata(raw string) (*openai.OpenAIAuthClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid authorized connection identity")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid authorized connection identity")
	}
	var claims struct {
		Auth *openai.OpenAIAuthClaims `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Auth == nil || claims.Auth.ChatGPTAccountID == "" {
		return nil, errors.New("authorized connection workspace is missing")
	}
	return claims.Auth, nil
}
