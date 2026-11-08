//go:build integration

package credentialops_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

type credentialSeamReader struct{ account *service.Account }

func (r credentialSeamReader) GetAccount(context.Context, int64) (*service.Account, error) {
	return r.account, nil
}
func (r credentialSeamReader) GetProxy(context.Context, int64) (*service.Proxy, error) {
	return nil, nil
}

type credentialSeamEncryptor struct{}

func (credentialSeamEncryptor) Encrypt(value string) (string, error) { return value, nil }
func (credentialSeamEncryptor) Decrypt(value string) (string, error) { return value, nil }

type credentialSeamProber struct{}

func (credentialSeamProber) FetchOpenAIModelsListFresh(context.Context, *service.Account) (*service.OpenAIModelsResponse, error) {
	return nil, errors.New("account_retired")
}

func TestCredentialOperationWorkerStorageAPISeam(t *testing.T) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("credential_test"), tcpostgres.WithUsername("fixture"), tcpostgres.WithPassword("synthetic"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Terminate(context.Background())) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	integrationDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = integrationDB.Close() })
	require.NoError(t, repository.ApplyMigrations(ctx, integrationDB))
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, integrationDB)))
	accountRepo := repository.NewAccountRepository(client, integrationDB, nil)
	fixturePath := os.Getenv("CREDENTIAL_OPERATION_WORKER_FIXTURE")
	var cases []struct {
		Name         string `json:"name"`
		Code         string `json:"code"`
		WorkerReason string `json:"worker_reason"`
	}
	if fixturePath == "" {
		// The ordinary integration suite still verifies storage/API. The
		// dedicated CI supplies actual offline worker output for every fixture.
		cases = append(cases, struct {
			Name         string `json:"name"`
			Code         string `json:"code"`
			WorkerReason string `json:"worker_reason"`
		}{
			Name: "retired", Code: "account_retired", WorkerReason: "local_protocol_failed stage=web_login reason=account_retired elapsed_ms=120 exit=1",
		})
	} else {
		raw, err := os.ReadFile(fixturePath)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &cases))
		require.Greater(t, len(cases), 20)
	}
	var responses []json.RawMessage
	for _, fixture := range cases {
		t.Run(fixture.Name, func(t *testing.T) {
			account := &service.Account{
				Name: "fixture-" + fixture.Name, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Credentials: map[string]any{"access_token": "synthetic-access", "refresh_token": "synthetic-refresh"},
				Status:      service.StatusActive, Schedulable: true, Concurrency: 1, Priority: 50,
			}
			require.NoError(t, accountRepo.Create(ctx, account))
			t.Cleanup(func() {
				_, err := integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, account.ID)
				require.NoError(t, err)
			})
			guardRepo := repository.NewAccountTokenGuardV2Repository(integrationDB)
			require.NoError(t, guardRepo.UpsertAccount(ctx, account.ID, false, false))
			reauthRepo := repository.NewOpenAIOAuthReauthRepository(integrationDB)
			require.NoError(t, reauthRepo.UpsertConfig(ctx, &service.OpenAIOAuthReauthStoredConfig{
				AccountID: account.ID, LoginEmail: "fixture", CredentialMode: service.OpenAIOAuthReauthModePasswordTOTP,
				ProxySource: service.OpenAIOAuthReauthProxySourceAccount, PasswordCiphertext: "synthetic-ciphertext",
			}))
			created, err := reauthRepo.CreateTask(ctx, account.ID, "synthetic-hash")
			require.NoError(t, err)
			running, err := reauthRepo.ClaimNextTask(ctx, "fixture-worker", time.Minute)
			require.NoError(t, err)
			require.Equal(t, created.ID, running.ID)
			reader := credentialSeamReader{account}
			updater := accountRepo.(service.OpenAIOAuthReauthCredentialUpdater)
			reauth := service.NewOpenAIOAuthReauthService(reauthRepo, reader, updater, service.NewOpenAIOAuthService(nil, nil), credentialSeamEncryptor{}, true, nil, nil)
			require.Error(t, reauth.FailTask(ctx, created.ID, "wrong-owner", fixture.WorkerReason))
			require.NoError(t, reauth.FailTask(ctx, created.ID, "fixture-worker", fixture.WorkerReason))
			stored, err := reauthRepo.GetLatestTask(ctx, account.ID)
			require.NoError(t, err)
			require.Equal(t, fixture.WorkerReason, stored.Error)
			require.NotNil(t, stored.FinishedAt)
			// A different current status must never reclassify the historical task.
			account.Status = service.StatusActive
			account.ErrorMessage = "synthetic-private"
			guard := service.NewAccountTokenGuardV2Service(guardRepo, nil, reader, credentialSeamProber{}, reauth)
			view, err := guard.ProbeNow(ctx, account.ID)
			require.NoError(t, err)
			require.Equal(t, "account_retired", view.ProbeFailure.Code)
			require.Equal(t, fixture.Code, view.LatestTask.Failure.Code)
			require.Equal(t, stored.FinishedAt, view.LatestTask.Failure.OccurredAt)
			recorder := guardRepo.(service.CredentialOperationRefreshRecorder)
			at := time.Date(2026, 10, 1, 1, 2, 3, 0, time.UTC)
			require.NoError(t, recorder.RecordRefreshFailure(ctx, account.ID, fixture.Code, at))
			require.NoError(t, recorder.RecordRefreshFailure(ctx, account.ID, "password_rejected", at.Add(-time.Hour)))
			saved, err := guardRepo.GetAccount(ctx, account.ID)
			require.NoError(t, err)
			require.Equal(t, fixture.Code, saved.RefreshFailureCode)
			require.False(t, saved.Enabled)
			require.False(t, saved.AutoReloginEnabled)
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest("GET", "/admin/account-ops/token-guard-v2/accounts", nil)
			admin.NewAccountTokenGuardV2Handler(guard).List(context)
			require.Equal(t, 200, response.Code)
			require.NotContains(t, response.Body.String(), "synthetic-private")
			require.Contains(t, response.Body.String(), `"probe_failure"`)
			require.Contains(t, response.Body.String(), `"refresh_failure"`)
			responses = append(responses, append(json.RawMessage(nil), response.Body.Bytes()...))
			// Remove before the next List so each response has exactly one row.
			require.NoError(t, guardRepo.DeleteAccount(ctx, account.ID))
		})
	}
	if output := os.Getenv("CREDENTIAL_OPERATION_API_FIXTURE"); output != "" {
		raw, err := json.MarshalIndent(responses, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(output, raw, 0600))
	}
}
