//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type primaryOnlyGeminiAccounts struct {
	AccountRepository
	accounts    []Account
	queryGroups []int64
}

func (r *primaryOnlyGeminiAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			return &r.accounts[i], nil
		}
	}
	return nil, nil
}

func (r *primaryOnlyGeminiAccounts) ListSchedulableByGroupIDAndPlatforms(_ context.Context, groupID int64, platforms []string) ([]Account, error) {
	r.queryGroups = append(r.queryGroups, groupID)
	var accounts []Account
	for _, account := range r.accounts {
		for _, platform := range platforms {
			belongs := false
			for _, id := range account.GroupIDs {
				belongs = belongs || id == groupID
			}
			if account.Platform == platform && belongs {
				accounts = append(accounts, account)
			}
		}
	}
	return accounts, nil
}

func (r *primaryOnlyGeminiAccounts) ListSchedulableByPlatforms(_ context.Context, _ []string) ([]Account, error) {
	r.queryGroups = append(r.queryGroups, 0)
	return r.accounts, nil
}

func primaryOnlySiblingContext(t *testing.T, platform string) (context.Context, int64) {
	t.Helper()
	groupID := int64(1)
	key, err := primaryGroupOnly(&APIKey{GroupID: &groupID, GroupIDs: []int64{1, 2}, Group: &Group{ID: 1, Platform: platform, Status: StatusActive, Hydrated: true}})
	require.NoError(t, err)
	ctx := WithAPIKeyGroupConstraint(context.Background(), key)
	return context.WithValue(ctx, ctxkey.Group, key.Group), groupID
}

func TestIssue1081SimpleGeminiForcedPlatformNeverFallsBackToExtraGroup(t *testing.T) {
	ctx, groupID := primaryOnlySiblingContext(t, PlatformGemini)
	ctx = context.WithValue(ctx, ctxkey.ForcePlatform, PlatformGemini)
	extra := Account{ID: 22, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{2}, AccountGroups: []AccountGroup{{GroupID: 2}}}
	require.True(t, extra.IsModelSupported("gemini-2.5-flash"))
	repo := &primaryOnlyGeminiAccounts{accounts: []Account{extra}}
	svc := &GeminiMessagesCompatService{accountRepo: repo, cfg: &config.Config{RunMode: config.RunModeSimple}}
	selected, err := svc.SelectAccountForModel(ctx, &groupID, "", "gemini-2.5-flash")
	require.Error(t, err)
	require.Nil(t, selected)
	require.Equal(t, []int64{1}, repo.queryGroups, "empty primary must not trigger platform-wide fallback")
}

func TestIssue1081SimpleGeminiRejectsBetterExtraStickyAndUsageProbe(t *testing.T) {
	ctx, groupID := primaryOnlySiblingContext(t, PlatformGemini)
	main := Account{ID: 11, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 9, GroupIDs: []int64{1}, AccountGroups: []AccountGroup{{GroupID: 1}}}
	extra := Account{ID: 22, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 0, GroupIDs: []int64{2}, AccountGroups: []AccountGroup{{GroupID: 2}}}
	cache := &mockGatewayCacheForGemini{sessionBindings: map[string]int64{"gemini:sticky": extra.ID}}
	repo := &primaryOnlyGeminiAccounts{accounts: []Account{main, extra}}
	svc := &GeminiMessagesCompatService{accountRepo: repo, cache: cache, cfg: &config.Config{RunMode: config.RunModeSimple}, rateLimitService: &RateLimitService{}}
	selected, err := svc.SelectAccountForModel(ctx, &groupID, "sticky", "gemini-2.5-flash")
	require.NoError(t, err)
	require.Equal(t, main.ID, selected.ID)
	require.Equal(t, main.ID, cache.sessionBindings["gemini:sticky"])
	precheck := svc.buildPreCheckUsageResultMap(ctx, []Account{main, extra}, "gemini-2.5-flash")
	require.Equal(t, map[int64]bool{main.ID: true}, precheck)
}

func TestIssue1081SimpleGeminiPrimaryAndLegacyRequestsStillWrite(t *testing.T) {
	for _, scenario := range []string{"projected primary", "original single group", "policy enabled"} {
		t.Run(scenario, func(t *testing.T) {
			groupID := int64(1)
			key := &APIKey{GroupID: &groupID, GroupIDs: []int64{1, 2}, Group: &Group{ID: 1, Platform: PlatformGemini, Status: StatusActive, Hydrated: true}}
			var err error
			if scenario == "original single group" {
				key.GroupIDs = []int64{1}
			}
			if scenario != "policy enabled" {
				key, err = primaryGroupOnly(key)
				require.NoError(t, err)
			}
			ctx := WithAPIKeyGroupConstraint(context.Background(), key)
			ctx = context.WithValue(ctx, ctxkey.Group, key.Group)
			groups := []int64{2}
			if scenario == "projected primary" {
				groups = []int64{1}
			}
			account := &Account{ID: 11, Platform: PlatformGemini, Type: AccountTypeAPIKey, GroupIDs: groups, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://upstream.invalid"}}
			upstream := &primaryOnlySiblingUpstream{}
			svc := &GeminiMessagesCompatService{cfg: &config.Config{RunMode: config.RunModeSimple}, httpUpstream: upstream}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", nil).WithContext(ctx)
			result, err := svc.ForwardNative(ctx, c, account, "gemini-2.5-flash", "generateContent", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 1, upstream.writes)
		})
	}
}

type primaryOnlySiblingUpstream struct{ writes int }

func (u *primaryOnlySiblingUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.writes++
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`))}, nil
}

func (u *primaryOnlySiblingUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func TestIssue1081SimpleSiblingAdmissionStopsBeforeFirstUpstreamWrite(t *testing.T) {
	ctx, _ := primaryOnlySiblingContext(t, PlatformGemini)
	account := &Account{ID: 22, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{2}, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://upstream.invalid"}}
	upstream := &primaryOnlySiblingUpstream{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	svc := &GeminiMessagesCompatService{cfg: cfg, httpUpstream: upstream}
	gateway := &GatewayService{cfg: cfg, httpUpstream: upstream}
	antigravity := &AntigravityGatewayService{httpUpstream: upstream}
	body := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hello"}]}`)
	nativeBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	parsed := &ParsedRequest{Model: "gemini-2.5-flash", Body: NewRequestBodyRef(body)}
	cases := map[string]func(*gin.Context) error{
		"gemini native": func(c *gin.Context) error {
			_, err := svc.ForwardNative(ctx, c, account, "gemini-2.5-flash", "generateContent", false, nativeBody)
			return err
		},
		"gemini messages":  func(c *gin.Context) error { _, err := svc.Forward(ctx, c, account, body); return err },
		"gemini chat":      func(c *gin.Context) error { _, err := svc.ForwardAsChatCompletions(ctx, c, account, body); return err },
		"gemini responses": func(c *gin.Context) error { _, err := svc.ForwardAsResponses(ctx, c, account, body); return err },
		"generic messages": func(c *gin.Context) error { _, err := gateway.Forward(ctx, c, account, parsed); return err },
		"generic chat": func(c *gin.Context) error {
			_, err := gateway.ForwardAsChatCompletions(ctx, c, account, body, parsed)
			return err
		},
		"generic responses": func(c *gin.Context) error {
			_, err := gateway.ForwardAsResponses(ctx, c, account, body, parsed)
			return err
		},
		"generic count tokens": func(c *gin.Context) error { return gateway.ForwardCountTokens(ctx, c, account, parsed) },
		"antigravity messages": func(c *gin.Context) error { _, err := antigravity.Forward(ctx, c, account, body, false); return err },
		"antigravity native": func(c *gin.Context) error {
			_, err := antigravity.ForwardGemini(ctx, c, account, "gemini-2.5-flash", "generateContent", false, nativeBody, false)
			return err
		},
		"antigravity upstream": func(c *gin.Context) error { _, err := antigravity.ForwardUpstream(ctx, c, account, body); return err },
		"antigravity chat": func(c *gin.Context) error {
			_, err := antigravity.ForwardAsChatCompletions(ctx, c, account, body, parsed)
			return err
		},
		"antigravity responses": func(c *gin.Context) error {
			_, err := antigravity.ForwardAsResponses(ctx, c, account, body, parsed)
			return err
		},
	}
	for name, forward := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body))).WithContext(ctx)
			require.ErrorIs(t, forward(c), ErrAPIKeyPrimaryGroupRequired)
			require.Zero(t, upstream.writes)
		})
	}
	_, err := svc.ForwardListModels(ctx, account)
	require.ErrorIs(t, err, ErrAPIKeyPrimaryGroupRequired)
	_, err = svc.ForwardGetModel(ctx, account, "gemini-2.5-flash")
	require.ErrorIs(t, err, ErrAPIKeyPrimaryGroupRequired)
	cached := &GeminiCachedContentService{compat: svc}
	_, err = cached.doUpstream(ctx, account, http.MethodPost, "https://upstream.invalid/v1beta/cachedContents", nil, time.Second)
	require.ErrorIs(t, err, ErrAPIKeyPrimaryGroupRequired)
	require.ErrorIs(t, err, ErrGeminiCachedContentRequestNotSent)
	require.Zero(t, upstream.writes)
}
