package service

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"net/http"
	"strings"
	"testing"
)

type newAPIAccounts struct {
	*upstreamBillingProbeAccountRepo
}

func (r *newAPIAccounts) ListAllWithFilters(_ context.Context, platform, kind, _, _ string, _ int64, _ string) ([]Account, error) {
	out := []Account{}
	for _, a := range r.accounts {
		if (platform == "" || platform == a.Platform) && (kind == "" || kind == a.Type) {
			out = append(out, *a)
		}
	}
	return out, nil
}

type newAPIAuthRepoFake struct {
	NewAPIAuthorizationRepository
	profile  *NewAPISiteAuthorization
	bindings map[int64]*NewAPIAccountBinding
	saves    int
}

func (r *newAPIAuthRepoFake) GetProfile(context.Context, string, int64) (*NewAPISiteAuthorization, error) {
	return r.profile, nil
}
func (r *newAPIAuthRepoFake) GetBinding(_ context.Context, id int64) (*NewAPIAccountBinding, error) {
	return r.bindings[id], nil
}
func (r *newAPIAuthRepoFake) Save(_ context.Context, p *NewAPISiteAuthorization, b []NewAPIBindingSave) error {
	r.saves++
	r.profile = p
	for _, v := range b {
		r.bindings[v.Account.ID] = &NewAPIAccountBinding{Profile: *p, AccountID: v.Account.ID, TokenID: v.TokenID, Fingerprint: NewAPIAccountFingerprint(v.Account)}
	}
	return nil
}

type newAPIEncryptorFake struct{}

func (newAPIEncryptorFake) Encrypt(s string) (string, error) { return "encrypted-" + s, nil }
func (newAPIEncryptorFake) Decrypt(s string) (string, error) {
	return strings.TrimPrefix(s, "encrypted-"), nil
}
func newAPIConfigFixture() (*UpstreamBillingProbeService, *newAPIAuthRepoFake, *newAPIHTTP, *newAPIAccounts) {
	accounts := &newAPIAccounts{&upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{1: {ID: 1, Name: "first", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://example.com/v1", "api_key": "sk-abcd123456789wxyz"}}, 2: {ID: 2, Name: "peer", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://example.com", "api_key": "sk-nope123456789xxxx"}}, 3: {ID: 3, Name: "foreign", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://example.com/tenant", "api_key": "sk-abcd123456789wxyz"}}}}}
	h := &newAPIHTTP{bodies: map[string]string{"GET /api/status": `{"success":true,"data":{"quota_per_unit":500000}}`, "GET /api/user/self": `{"success":true,"data":{"id":7,"quota":1250000}}`, "GET /api/pricing": `{"success":true,"group_ratio":{"vip":0.5}}`, "GET /api/token/?p=0&page_size=100": `{"success":true,"data":{"items":[{"id":3,"key":"abcd**********wxyz","group":"vip","name":"mine"}],"total":1,"page":1,"page_size":100}}`}}
	s := newUpstreamBillingProbeTestService(accounts, h, nil)
	r := &newAPIAuthRepoFake{bindings: map[int64]*NewAPIAccountBinding{}}
	s.SetNewAPIAuthorization(r, newAPIEncryptorFake{}, true)
	return s, r, h, accounts
}
func TestNewAPIConfigNoSaveForForeignOrUnmatchedAccounts(t *testing.T) {
	for _, ids := range [][]int64{{1, 2}, {1, 3}, {2}, {1, 1}} {
		s, r, _, _ := newAPIConfigFixture()
		_, e := s.SaveNewAPIConfig(context.Background(), 1, NewAPIConfigRequest{UserID: 7, AccessToken: "secret", AccountIDs: ids})
		require.Error(t, e)
		require.Zero(t, r.saves)
		require.Nil(t, r.profile)
		require.Empty(t, r.bindings)
	}
}
func TestNewAPIGatewayAuthorizationUsesDeclaredProvider(t *testing.T) {
	s, auth, _, repo := newAPIConfigFixture()
	gateway := repo.accounts[1]
	gateway.Platform, gateway.Type = PlatformGateway, AccountTypeGateway
	gateway.Credentials[GatewayUpstreamKindCredentialKey] = "sub2api"
	_, err := s.GetNewAPIConfig(context.Background(), gateway.ID)
	require.ErrorIs(t, err, ErrUpstreamBillingProbeAccountInvalid)

	gateway.Credentials[GatewayUpstreamKindCredentialKey] = "new_api"
	preview, err := s.SaveNewAPIConfig(context.Background(), gateway.ID, NewAPIConfigRequest{UserID: 7, AccountIDs: []int64{gateway.ID}, AccessToken: "synthetic-management-token"})
	require.NoError(t, err)
	require.Len(t, preview.Accounts, 1)
	require.Nil(t, preview.Accounts[0].CostMultiplier)
	require.Equal(t, 2.5, preview.Wallet.Amount)
	config, err := s.GetNewAPIConfig(context.Background(), repo.accounts[2].ID)
	require.NoError(t, err)
	require.Len(t, config.Accounts, 2)
	require.NotNil(t, auth.bindings[gateway.ID])
}

func TestNewAPIConfigSharedEncryptedAuthAndSafeRead(t *testing.T) {
	s, r, h, accounts := newAPIConfigFixture()
	h.bodies["GET /api/token/?p=0&page_size=100"] = `{"success":true,"data":{"items":[{"id":3,"key":"abcd**********wxyz","group":"vip"},{"id":4,"key":"nope**********xxxx","group":"auto"}],"total":2,"page":1,"page_size":100}}`
	out, e := s.SaveNewAPIConfig(context.Background(), 1, NewAPIConfigRequest{UserID: 7, AccessToken: "private-management-secret", AccountIDs: []int64{1, 2}})
	require.NoError(t, e)
	require.Len(t, out.Accounts, 2)
	require.Equal(t, "encrypted-private-management-secret", r.profile.Ciphertext)
	require.Equal(t, r.bindings[1].Profile, r.bindings[2].Profile)
	cfg, e := s.GetNewAPIConfig(context.Background(), 1)
	require.NoError(t, e)
	require.Len(t, cfg.Accounts, 2)
	b, e := json.Marshal(cfg)
	require.NoError(t, e)
	require.NotContains(t, string(b), "secret")
	require.NotContains(t, string(b), "cipher")
	_, e = s.PreviewNewAPIConfig(context.Background(), 1, NewAPIConfigRequest{UserID: 7, AccountIDs: []int64{1}})
	require.NoError(t, e)
	accounts.accounts[1].Credentials["api_key"] = "changed"
	cfg, e = s.GetNewAPIConfig(context.Background(), 1)
	require.NoError(t, e)
	require.False(t, cfg.Configured)
}
func TestNewAPIAmbiguousMaskRequiresVerifiedManualSelection(t *testing.T) {
	s, r, h, _ := newAPIConfigFixture()
	h.bodies["GET /api/token/?p=0&page_size=100"] = `{"success":true,"data":{"items":[{"id":3,"key":"abcd**********wxyz","group":"vip"},{"id":4,"key":"abcd**********wxyz","group":"vip"}],"total":2,"page":1,"page_size":100}}`
	req := NewAPIConfigRequest{UserID: 7, AccessToken: "secret", AccountIDs: []int64{1}}
	out, e := s.PreviewNewAPIConfig(context.Background(), 1, req)
	require.NoError(t, e)
	require.False(t, out.Accounts[0].Matched)
	require.Len(t, out.Accounts[0].TokenOptions, 2)
	_, e = s.SaveNewAPIConfig(context.Background(), 1, req)
	require.Error(t, e)
	require.Zero(t, r.saves)
	req.TokenSelections = map[string]int64{"1": 4}
	h.bodies["POST /api/token/4/key"] = `{"success":true,"data":{"key":"sk-abcdFOREIGNwxyz"}}`
	_, e = s.SaveNewAPIConfig(context.Background(), 1, req)
	require.Error(t, e)
	require.Zero(t, r.saves)
	h.bodies["POST /api/token/4/key"] = `{"success":true,"data":{"key":"sk-abcd123456789wxyz"}}`
	_, e = s.SaveNewAPIConfig(context.Background(), 1, req)
	require.NoError(t, e)
	require.Equal(t, int64(4), r.bindings[1].TokenID)
}
func TestNewAPIConfigRequiresFixedEncryptionKey(t *testing.T) {
	s, r, _, _ := newAPIConfigFixture()
	s.newAPIFixedKey = false
	_, e := s.SaveNewAPIConfig(context.Background(), 1, NewAPIConfigRequest{UserID: 7, AccessToken: "secret", AccountIDs: []int64{1}})
	require.ErrorIs(t, e, ErrNewAPIEncryptionKey)
	require.Zero(t, r.saves)
}
func TestNewAPIChangedKeyBindingReturnsToLegacyProbeWithoutUserCredential(t *testing.T) {
	s, _, h, accounts := newAPIConfigFixture()
	_, e := s.SaveNewAPIConfig(context.Background(), 1, NewAPIConfigRequest{UserID: 7, AccessToken: "private-management-secret", AccountIDs: []int64{1}})
	require.NoError(t, e)
	accounts.accounts[1].Credentials["api_key"] = "sk-changed"
	h.check = func(r *http.Request) {
		require.NotEqual(t, "Bearer private-management-secret", r.Header.Get("Authorization"))
		require.Equal(t, "", r.Header.Get("New-Api-User"))
	}
	snap, e := s.ProbeAccount(context.Background(), 1)
	require.NoError(t, e)
	require.Equal(t, UpstreamBillingProbeStatusUnsupported, snap.Status)
}
func TestNewAPIFingerprintIgnoresModelMappingButGuardsTransport(t *testing.T) {
	_, _, _, r := newAPIConfigFixture()
	a := r.accounts[1]
	first := NewAPIAccountFingerprint(a)
	a.Credentials["model_mapping"] = map[string]any{"old": "new"}
	require.Equal(t, first, NewAPIAccountFingerprint(a))
	a.Credentials["base_url"] = "https://foreign.example"
	require.NotEqual(t, first, NewAPIAccountFingerprint(a))
}
func (r *newAPIAuthRepoFake) WriteSnapshot(_ context.Context, a *Account, _ *NewAPIAccountBinding, s *UpstreamBillingProbeSnapshot) error {
	a.Extra = map[string]any{UpstreamBillingProbeExtraKey: s}
	return nil
}
func TestNewAPIBoundKeyDisappearsPersistsFailureWithBackoff(t *testing.T) {
	s, r, h, accounts := newAPIConfigFixture()
	_, e := s.SaveNewAPIConfig(context.Background(), 1, NewAPIConfigRequest{UserID: 7, AccessToken: "secret", AccountIDs: []int64{1}})
	require.NoError(t, e)
	h.bodies["GET /api/token/?p=0&page_size=100"] = `{"success":true,"data":{"items":[],"total":0,"page":1,"page_size":100}}`
	snap, e := s.probeNewAPIAccount(context.Background(), accounts.accounts[1], r.bindings[1], 30)
	require.NoError(t, e)
	require.Equal(t, UpstreamBillingProbeStatusFailed, snap.Status)
	require.Equal(t, "new_api_bound_key_not_owned", snap.LastError)
	require.True(t, snap.NextProbeAt.After(snap.LastAttemptAt))
	require.Nil(t, snap.Data)
	require.Equal(t, UpstreamBillingProbeStatusFailed, snap.Balance.Status)
}
