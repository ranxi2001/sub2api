//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// This integration-only bridge exposes the isolated database and concrete
// journal through interfaces without importing the HTTP handler into repository.
type ImageBillingRecoveryTestRepository interface {
	service.UsageBillingRepository
	service.DurableUsageBillingRepository
	StageUsageBillingForReceipt(context.Context, *service.UsageBillingCommand, *service.UsageLog) (bool, error)
}

type ImageBillingRecoveryTestFixture struct {
	DB         *sql.DB
	Users      service.UserRepository
	Key        *service.APIKey
	Account    *service.Account
	NewJournal func() ImageBillingRecoveryTestRepository
}

func NewImageBillingRecoveryTestFixture(t *testing.T) ImageBillingRecoveryTestFixture {
	t.Helper()
	db, client := newBillingAuditDatabase(t)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.invalid", Balance: 100})
	group := mustCreateGroup(t, client, &service.Group{Name: "image-recovery", Platform: service.PlatformOpenAI, RateMultiplier: 1})
	group.AllowImageGeneration = true
	price := .01
	group.ModelPricing = []service.ChannelModelPricing{{
		Models: []string{"gpt-image-2"}, BillingMode: service.BillingModeToken,
		InputPrice: new(float64), OutputPrice: new(float64), ImageMinimumCharge: &price,
	}}
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Quota: 100})
	key.User, key.Group = user, group
	account := mustCreateAccount(t, client, &service.Account{
		Name: "image-recovery", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "fixture", "base_url": "https://fixture.example/v1"},
	})
	account.Concurrency = 0
	account.GroupIDs = []int64{group.ID}
	return ImageBillingRecoveryTestFixture{
		DB: db, Users: NewUserRepository(client, db), Key: key, Account: account,
		NewJournal: func() ImageBillingRecoveryTestRepository { return &usageBillingRepository{db: db} },
	}
}
