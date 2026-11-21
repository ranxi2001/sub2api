package service

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Secrets are deliberately absent from the JSON-facing DTOs below.
type NewAPISiteAuthorization struct {
	ID, UserID, Revision int64
	SiteURL              string
	Ciphertext           string `json:"-"`
}
type NewAPIAccountBinding struct {
	Profile            NewAPISiteAuthorization
	AccountID, TokenID int64
	Fingerprint, Group string
}
type NewAPIBindingSave struct {
	Account        *Account
	TokenID        int64
	Group          string
	ApplyUSDRate   *bool
	CostMultiplier *float64
}
type NewAPIAuthorizationRepository interface {
	GetProfile(context.Context, string, int64) (*NewAPISiteAuthorization, error)
	GetBinding(context.Context, int64) (*NewAPIAccountBinding, error)
	Save(context.Context, *NewAPISiteAuthorization, []NewAPIBindingSave) error
	Unbind(context.Context, int64) error
	WriteSnapshot(context.Context, *Account, *NewAPIAccountBinding, *UpstreamBillingProbeSnapshot) error
}
type NewAPIConfigRequest struct {
	UserID          int64            `json:"user_id"`
	AccessToken     string           `json:"access_token,omitempty"`
	AccountIDs      []int64          `json:"account_ids"`
	TokenSelections map[string]int64 `json:"token_selections,omitempty"`
	ApplyUSDRate    map[string]bool  `json:"apply_usd_rate,omitempty"`
}
type NewAPIConfigAccount struct {
	AccountID           int64   `json:"account_id"`
	Name                string  `json:"name"`
	ConfiguredUserID    *int64  `json:"configured_user_id,omitempty"`
	CostMultiplierScale float64 `json:"cost_multiplier_scale"`
	CNProvider          bool    `json:"cn_provider"`
	CostAutoSyncEnabled bool    `json:"cost_auto_sync_enabled"`
	ApplyUSDRate        bool    `json:"apply_usd_rate"`
}
type NewAPIConfig struct {
	AccountID               int64                 `json:"account_id"`
	SiteURL                 string                `json:"site_url"`
	Configured              bool                  `json:"configured"`
	UserID                  *int64                `json:"user_id,omitempty"`
	EncryptionKeyConfigured bool                  `json:"encryption_key_configured"`
	Accounts                []NewAPIConfigAccount `json:"accounts"`
}
type NewAPITokenOption struct {
	TokenID   int64  `json:"token_id"`
	Name      string `json:"name"`
	Group     string `json:"group"`
	MaskedKey string `json:"masked_key"`
}
type NewAPIPreviewAccount struct {
	AccountID           int64               `json:"account_id"`
	Name                string              `json:"name"`
	Matched             bool                `json:"matched"`
	TokenID             *int64              `json:"token_id,omitempty"`
	Group               string              `json:"group,omitempty"`
	Rate                *float64            `json:"rate,omitempty"`
	CostMultiplierScale float64             `json:"cost_multiplier_scale"`
	CNProvider          bool                `json:"cn_provider"`
	CostAutoSyncEnabled bool                `json:"cost_auto_sync_enabled"`
	ApplyUSDRate        bool                `json:"apply_usd_rate"`
	CostMultiplier      *float64            `json:"cost_multiplier,omitempty"`
	Error               string              `json:"error,omitempty"`
	TokenOptions        []NewAPITokenOption `json:"token_options"`
}
type NewAPIPreview struct {
	SiteURL string `json:"site_url"`
	UserID  int64  `json:"user_id"`
	Wallet  struct {
		Amount float64 `json:"amount"`
		Unit   string  `json:"unit"`
	} `json:"wallet"`
	Accounts []NewAPIPreviewAccount `json:"accounts"`
}

func (s *UpstreamBillingProbeService) SetNewAPIAuthorization(repo NewAPIAuthorizationRepository, encryptor SecretEncryptor, fixedKey bool) {
	s.newAPIRepo = repo
	s.newAPIEncryptor = encryptor
	s.newAPIFixedKey = fixedKey
}
func (s *UpstreamBillingProbeService) newAPIAccount(ctx context.Context, id int64) (*Account, string, error) {
	if s == nil || s.accountRepo == nil || s.newAPIRepo == nil {
		return nil, "", ErrUpstreamBillingProbeUnavailable
	}
	a, e := s.accountRepo.GetByID(ctx, id)
	if e != nil {
		return nil, "", e
	}
	if !isUpstreamBillingProbeAccount(a) || a.GetCredential("api_key") == "" ||
		(a.IsGateway() && a.GetCredential(GatewayUpstreamKindCredentialKey) != "new_api") {
		return nil, "", ErrUpstreamBillingProbeAccountInvalid
	}
	site, e := CanonicalNewAPISite(a.GetCredential("base_url"))
	return a, site, e
}
func (s *UpstreamBillingProbeService) GetNewAPIConfig(ctx context.Context, id int64) (*NewAPIConfig, error) {
	a, site, e := s.newAPIAccount(ctx, id)
	if e != nil {
		return nil, e
	}
	out := &NewAPIConfig{AccountID: id, SiteURL: site, EncryptionKeyConfigured: s.newAPIFixedKey, Accounts: []NewAPIConfigAccount{}}
	accounts, e := s.accountRepo.ListAllWithFilters(ctx, "", AccountTypeAPIKey, "", "", 0, "")
	if e != nil {
		return nil, e
	}
	gateways, e := s.accountRepo.ListAllWithFilters(ctx, PlatformGateway, AccountTypeGateway, "", "", 0, "")
	if e != nil {
		return nil, e
	}
	for i := range gateways {
		if gateways[i].IsGateway() && gateways[i].GetCredential(GatewayUpstreamKindCredentialKey) == "new_api" {
			accounts = append(accounts, gateways[i])
		}
	}
	found := false
	for i := range accounts {
		if accounts[i].ID == id {
			found = true
		}
	}
	if !found {
		accounts = append(accounts, *a)
	}
	for i := range accounts {
		peer := &accounts[i]
		peerSite, e := CanonicalNewAPISite(peer.GetCredential("base_url"))
		if e != nil || peerSite != site || peer.GetCredential("api_key") == "" {
			continue
		}
		item := NewAPIConfigAccount{AccountID: peer.ID, Name: peer.Name, CostMultiplierScale: NewAPICostScale(peer), CNProvider: IsCNProvider(peer.Platform), CostAutoSyncEnabled: peer.CostMultiplierAutoSyncEnabled(), ApplyUSDRate: NewAPIApplyUSDRate(peer)}
		binding, e := s.newAPIRepo.GetBinding(ctx, peer.ID)
		if e != nil {
			return nil, newAPIError("storage_unavailable")
		}
		if binding != nil && binding.Profile.SiteURL == site && binding.Fingerprint == NewAPIAccountFingerprint(peer) {
			u := binding.Profile.UserID
			item.ConfiguredUserID = &u
			if peer.ID == id {
				out.Configured = true
				out.UserID = &u
			}
		}
		out.Accounts = append(out.Accounts, item)
	}
	sort.Slice(out.Accounts, func(i, j int) bool { return out.Accounts[i].AccountID < out.Accounts[j].AccountID })
	return out, nil
}
func (s *UpstreamBillingProbeService) PreviewNewAPIConfig(ctx context.Context, id int64, req NewAPIConfigRequest) (*NewAPIPreview, error) {
	out, _, _, e := s.verifyNewAPIConfig(ctx, id, req)
	return out, e
}
func (s *UpstreamBillingProbeService) SaveNewAPIConfig(ctx context.Context, id int64, req NewAPIConfigRequest) (*NewAPIPreview, error) {
	out, profile, bindings, e := s.verifyNewAPIConfig(ctx, id, req)
	if e != nil {
		return nil, e
	}
	for _, a := range out.Accounts {
		if !a.Matched {
			return nil, ErrNewAPIConfiguration
		}
	}
	if e = s.newAPIRepo.Save(ctx, profile, bindings); e != nil {
		return nil, newAPIError("configuration_changed_or_storage_unavailable")
	}
	return out, nil
}
func (s *UpstreamBillingProbeService) DeleteNewAPIConfig(ctx context.Context, id int64) error {
	if s == nil || s.newAPIRepo == nil {
		return ErrUpstreamBillingProbeUnavailable
	}
	if e := s.newAPIRepo.Unbind(ctx, id); e != nil {
		return newAPIError("storage_unavailable")
	}
	return nil
}
func (s *UpstreamBillingProbeService) verifyNewAPIConfig(ctx context.Context, id int64, req NewAPIConfigRequest) (*NewAPIPreview, *NewAPISiteAuthorization, []NewAPIBindingSave, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, site, e := s.newAPIAccount(ctx, id)
	if e != nil {
		return nil, nil, nil, e
	}
	if !s.newAPIFixedKey || s.newAPIEncryptor == nil {
		return nil, nil, nil, ErrNewAPIEncryptionKey
	}
	if req.UserID <= 0 || len(req.AccountIDs) == 0 || len(req.AccountIDs) > 100 || len(req.AccessToken) > 8192 {
		return nil, nil, nil, ErrNewAPIConfiguration
	}
	for id := range req.ApplyUSDRate {
		matched := false
		for _, accountID := range req.AccountIDs {
			if id == strconv.FormatInt(accountID, 10) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, nil, nil, ErrNewAPIConfiguration
		}
	}
	profile, e := s.newAPIRepo.GetProfile(ctx, site, req.UserID)
	if e != nil {
		return nil, nil, nil, newAPIError("storage_unavailable")
	}
	if profile == nil {
		profile = &NewAPISiteAuthorization{SiteURL: site, UserID: req.UserID}
	}
	secret := strings.TrimSpace(req.AccessToken)
	if secret == "" {
		if profile.Ciphertext == "" {
			return nil, nil, nil, ErrNewAPIConfiguration
		}
		secret, e = s.newAPIEncryptor.Decrypt(profile.Ciphertext)
		if e != nil {
			return nil, nil, nil, newAPIError("authorization_unavailable")
		}
	}
	if strings.ContainsAny(secret, "\r\n") {
		return nil, nil, nil, ErrNewAPIConfiguration
	}
	out := &NewAPIPreview{SiteURL: site, UserID: req.UserID, Accounts: []NewAPIPreviewAccount{}}
	out.Wallet.Unit = "USD"
	bindings := []NewAPIBindingSave{}
	seen := map[int64]bool{}
	hasPath := false
	// Each selected account is verified through its own proxy and header route.
	for _, accountID := range req.AccountIDs {
		if accountID <= 0 || seen[accountID] {
			return nil, nil, nil, ErrNewAPIConfiguration
		}
		seen[accountID] = true
		if accountID == id {
			hasPath = true
		}
		a, peerSite, e := s.newAPIAccount(ctx, accountID)
		if e != nil || peerSite != site {
			return nil, nil, nil, ErrNewAPIConfiguration
		}
		data, e := s.fetchNewAPI(ctx, a, site, req.UserID, secret)
		if e != nil {
			return nil, nil, nil, e
		}
		out.Wallet.Amount = data.wallet
		out.Wallet.Unit = data.walletUnit
		applyUSDRate := NewAPIApplyUSDRate(a)
		if value, ok := req.ApplyUSDRate[strconv.FormatInt(accountID, 10)]; ok {
			applyUSDRate = value
		}
		scale := newAPICostScaleFor(a, applyUSDRate)
		item := NewAPIPreviewAccount{AccountID: a.ID, Name: a.Name, TokenOptions: []NewAPITokenOption{}, CostMultiplierScale: scale, CNProvider: IsCNProvider(a.Platform), CostAutoSyncEnabled: a.CostMultiplierAutoSyncEnabled(), ApplyUSDRate: applyUSDRate}
		matches := matchNewAPITokens(a, data.tokens)
		selected := req.TokenSelections[strconv.FormatInt(accountID, 10)]
		var token *NewAPIToken
		for i := range matches {
			t := &matches[i]
			item.TokenOptions = append(item.TokenOptions, NewAPITokenOption{TokenID: t.ID, Name: sanitizeUpstreamBalanceText(t.Name, 128), Group: sanitizeUpstreamBalanceText(t.Group, 128), MaskedKey: maskNewAPIKey(t.Key)})
			if selected == t.ID {
				token = t
			}
		}
		if selected == 0 && len(matches) == 1 {
			token = &matches[0]
		}
		if selected != 0 && token == nil {
			item.Error = "token_selection_not_matched"
		} else if token == nil {
			if len(matches) > 1 {
				item.Error = "ambiguous_key"
			} else {
				item.Error = "key_not_owned"
			}
		} else {
			// Ambiguous masks require the owner's raw-key endpoint to prove selection.
			if len(matches) > 1 {
				var raw struct {
					Success bool `json:"success"`
					Data    struct {
						Key string `json:"key"`
					} `json:"data"`
				}
				e = s.newAPIRequest(ctx, a, site, req.UserID, secret, "POST", fmtNewAPIKeyPath(token.ID), &raw)
				if e != nil || !raw.Success || newAPIKey(raw.Data.Key) != newAPIKey(a.GetCredential("api_key")) {
					item.Error = "selected_key_verification_failed"
					token = nil
				}
			}
			if token != nil {
				item.Matched = true
				tID := token.ID
				item.TokenID = &tID
				item.Group = sanitizeUpstreamBalanceText(token.Group, 128)
				if r, ok := data.ratios[token.Group]; ok && token.Group != "auto" && !token.CrossGroupRetry && len(token.RouteGroups) <= 1 {
					item.Rate = &r
				}
				if a.CostMultiplierAutoSyncEnabled() {
					if cost, ok := NewAPICostToSync(newAPISnapshot(a, *token, data, 30, s.currentTime().UTC()), scale); ok {
						item.CostMultiplier = &cost
					}
				}
				bindings = append(bindings, NewAPIBindingSave{Account: a, TokenID: token.ID, Group: token.Group, ApplyUSDRate: &applyUSDRate, CostMultiplier: item.CostMultiplier})
			}
		}
		out.Accounts = append(out.Accounts, item)
	}
	if !hasPath {
		return nil, nil, nil, ErrNewAPIConfiguration
	}
	cipher, e := s.newAPIEncryptor.Encrypt(secret)
	if e != nil {
		return nil, nil, nil, newAPIError("encryption_failed")
	}
	copyProfile := *profile
	copyProfile.Ciphertext = cipher
	return out, &copyProfile, bindings, nil
}
func fmtNewAPIKeyPath(id int64) string { return "/api/token/" + strconv.FormatInt(id, 10) + "/key" }
func (s *UpstreamBillingProbeService) probeNewAPIAccount(ctx context.Context, a *Account, b *NewAPIAccountBinding, interval int) (*UpstreamBillingProbeSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !s.newAPIFixedKey || s.newAPIEncryptor == nil {
		return nil, ErrNewAPIEncryptionKey
	}
	site, e := CanonicalNewAPISite(a.GetCredential("base_url"))
	if e != nil || site != b.Profile.SiteURL || b.Fingerprint != NewAPIAccountFingerprint(a) {
		return nil, ErrUpstreamBillingProbeIdentityChanged
	}
	secret, e := s.newAPIEncryptor.Decrypt(b.Profile.Ciphertext)
	if e != nil {
		return nil, newAPIError("authorization_unavailable")
	}
	now := s.currentTime().UTC()
	data, e := s.fetchNewAPI(ctx, a, site, b.Profile.UserID, secret)
	var snap *UpstreamBillingProbeSnapshot
	if e != nil {
		snap = upstreamBillingProbeFailureSnapshot(a, interval, now, 0, "new_api_request_failed", 0)
		snap.Data = nil
		snap.Balance = &UpstreamBalanceSnapshot{Status: UpstreamBillingProbeStatusFailed, LastAttemptAt: now, LastError: "new_api_request_failed"}
	} else {
		matches := matchNewAPITokens(a, data.tokens)
		var matched *NewAPIToken
		for i := range matches {
			if matches[i].ID == b.TokenID {
				matched = &matches[i]
			}
		}
		if matched == nil {
			return s.persistNewAPIFailure(ctx, a, b, interval, now, "new_api_bound_key_not_owned")
		}
		if len(matches) > 1 {
			var raw struct {
				Success bool `json:"success"`
				Data    struct {
					Key string `json:"key"`
				} `json:"data"`
			}
			if e = s.newAPIRequest(ctx, a, site, b.Profile.UserID, secret, "POST", fmtNewAPIKeyPath(b.TokenID), &raw); e != nil || !raw.Success || newAPIKey(raw.Data.Key) != newAPIKey(a.GetCredential("api_key")) {
				return s.persistNewAPIFailure(ctx, a, b, interval, now, "new_api_bound_key_not_owned")
			}
		}
		snap = newAPISnapshot(a, *matched, data, interval, now)
	}
	if e = s.newAPIRepo.WriteSnapshot(ctx, a, b, snap); e != nil {
		return nil, ErrUpstreamBillingProbeIdentityChanged
	}
	return snap, nil
}

func (s *UpstreamBillingProbeService) persistNewAPIFailure(ctx context.Context, a *Account, b *NewAPIAccountBinding, interval int, now time.Time, reason string) (*UpstreamBillingProbeSnapshot, error) {
	snap := upstreamBillingProbeFailureSnapshot(a, interval, now, 0, reason, 0)
	snap.Data = nil
	snap.Balance = &UpstreamBalanceSnapshot{Status: UpstreamBillingProbeStatusFailed, LastAttemptAt: now, LastError: reason}
	if err := s.newAPIRepo.WriteSnapshot(ctx, a, b, snap); err != nil {
		return nil, ErrUpstreamBillingProbeIdentityChanged
	}
	return snap, nil
}
