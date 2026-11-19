//go:build integration

package repository_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type imageRecoveryBarrierRepo struct {
	repository.ImageBillingRecoveryTestRepository
	staged     chan service.UsageBillingEventKey
	resume     chan struct{}
	foreground *service.UsageBillingRecovery
}

func (r *imageRecoveryBarrierRepo) StageUsageBillingForReceipt(ctx context.Context, cmd *service.UsageBillingCommand, usage *service.UsageLog) (bool, error) {
	created, err := r.ImageBillingRecoveryTestRepository.StageUsageBillingForReceipt(ctx, cmd, usage)
	if err != nil {
		return false, err
	}
	select {
	case r.staged <- service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	select {
	case <-r.resume:
		return created, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (r *imageRecoveryBarrierRepo) ApplyStagedUsageBilling(ctx context.Context, key service.UsageBillingEventKey) (*service.UsageBillingRecovery, error) {
	recovered, err := r.ImageBillingRecoveryTestRepository.ApplyStagedUsageBilling(ctx, key)
	r.foreground = recovered
	return recovered, err
}

type imageRecoveryAccounts struct {
	service.AccountRepository
	account *service.Account
}

func (r imageRecoveryAccounts) GetByID(context.Context, int64) (*service.Account, error) {
	copy := *r.account
	return &copy, nil
}

func (r imageRecoveryAccounts) GetOpenAITurnAdmission(ctx context.Context, id int64) (*service.Account, *service.Account, error) {
	a, err := r.GetByID(ctx, id)
	return a, nil, err
}

func (r imageRecoveryAccounts) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]service.Account, error) {
	return []service.Account{*r.account}, nil
}

func (r imageRecoveryAccounts) ListSchedulableByPlatform(context.Context, string) ([]service.Account, error) {
	return []service.Account{*r.account}, nil
}

func (r imageRecoveryAccounts) ListSchedulableUngroupedByPlatform(context.Context, string) ([]service.Account, error) {
	return []service.Account{*r.account}, nil
}

type imageRecoveryTransport struct {
	service.HTTPUpstream
	target *url.URL
	client *http.Client
}

func (u imageRecoveryTransport) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = u.target.Scheme, u.target.Host
	return u.client.Do(req)
}

func TestImageReceiptRecoveryWinsBeforeForegroundApply(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	fixture := repository.NewImageBillingRecoveryTestFixture(t)
	db, user, key, account := fixture.DB, fixture.Key.User, fixture.Key, fixture.Account
	accounts := imageRecoveryAccounts{account: account}
	journal := fixture.NewJournal()
	barrier := &imageRecoveryBarrierRepo{ImageBillingRecoveryTestRepository: journal, staged: make(chan service.UsageBillingEventKey, 1), resume: make(chan struct{})}
	var resume sync.Once
	unblock := func() { resume.Do(func() { close(barrier.resume) }) }
	defer unblock()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "image-recovery-"+uuid.NewString())
		_, _ = io.WriteString(w, `{"data":[{"b64_json":"ZmluYWw="}],"usage":{"input_tokens":0,"output_tokens":0}}`)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Runtime.Role = config.RuntimeRoleGateway
	users := fixture.Users
	cache := service.NewBillingCacheService(nil, users, nil, nil, nil, nil, cfg, nil)
	defer cache.Stop()
	pricing := service.NewBillingService(cfg, nil)
	gateway := service.NewOpenAIGatewayService(accounts, nil, nil, barrier, users, nil, nil, nil, cfg,
		nil, nil, pricing, nil, cache, imageRecoveryTransport{target: target, client: upstream.Client()},
		nil, nil, nil, service.NewModelPricingResolver(nil, pricing), nil, nil, nil, nil)
	h := handler.NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), cache,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"model":"gpt-image-2","prompt":"fixture"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Image-Usage", "billing")
	c.Set(string(middleware.ContextKeyAPIKey), key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: user.ID})
	finished := make(chan struct{})
	go func() { defer close(finished); h.Images(c) }()
	var eventKey service.UsageBillingEventKey
	select {
	case eventKey = <-barrier.staged:
	case <-finished:
		t.Fatalf("handler returned before staging: %s", rec.Body.String())
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not reach durable stage")
	}
	// A separate repository instance wins while the foreground is held after
	// its Stage commit, then completes the worker's cache acknowledgement.
	worker := fixture.NewJournal()
	recovered, err := worker.ApplyStagedUsageBilling(ctx, eventKey)
	require.NoError(t, err)
	require.True(t, recovered.Result.Applied)
	require.True(t, recovered.Settled)
	require.True(t, recovered.NeedsCache)
	require.NoError(t, worker.AcknowledgeUsageBillingCache(ctx, eventKey))
	unblock()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("foreground did not finish after worker acknowledgement")
	}
	require.NotNil(t, barrier.foreground)
	require.False(t, barrier.foreground.Result.Applied)
	require.True(t, barrier.foreground.Settled)
	require.False(t, barrier.foreground.NeedsCache)
	require.EqualValues(t, 1, calls.Load())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "ZmluYWw=", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
	require.EqualValues(t, 1_000_000, gjson.Get(rec.Body.String(), "usage.output_tokens").Int())
	require.Equal(t, .01, gjson.Get(rec.Body.String(), "billing.actual_cost").Float())
	var balance, quota, state string
	var usageRows, claims int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT balance::text FROM users WHERE id=$1", user.ID).Scan(&balance))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT quota_used::text FROM api_keys WHERE id=$1", key.ID).Scan(&quota))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", eventKey.RequestID, key.ID).Scan(&usageRows))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2", eventKey.RequestID, key.ID).Scan(&claims))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT state FROM usage_billing_events WHERE request_id=$1 AND api_key_id=$2", eventKey.RequestID, key.ID).Scan(&state))
	require.Equal(t, "99.99000000", balance)
	require.Equal(t, "0.01000000", quota)
	require.Equal(t, 1, usageRows)
	require.Equal(t, 1, claims)
	require.Equal(t, "complete", state)
	created, err := journal.StageUsageBillingForReceipt(ctx, &recovered.Event.Command, &recovered.Event.Usage)
	require.NoError(t, err)
	require.False(t, created, "an identical older event cannot authorize a new response")
}
