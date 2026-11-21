//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountUpdatePreservesConcurrentProbeSnapshot(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	account := mustCreateAccount(t, tx.Client(), &service.Account{
		Name:        "probe-update-preserve",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-old"},
		Extra:       map[string]any{service.UpstreamBillingProbeEnabledExtraKey: true},
	})

	stale, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotContains(t, stale.Extra, service.UpstreamBillingProbeExtraKey)
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, stale, &service.UpstreamBillingProbeSnapshot{
		Status:        service.UpstreamBillingProbeStatusOK,
		LastAttemptAt: time.Now().UTC(),
	}, nil))

	stale.Name = "ordinary-edit"
	require.NoError(t, repo.Update(ctx, stale))
	got, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	snapshot, ok := got.Extra[service.UpstreamBillingProbeExtraKey].(map[string]any)
	require.True(t, ok)
	require.Equal(t, service.UpstreamBillingProbeStatusOK, snapshot["status"])

	require.NoError(t, repo.UpdateExtra(ctx, got.ID, map[string]any{service.UpstreamBillingProbeEnabledExtraKey: false}))
	disabled, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotContains(t, disabled.Extra, service.UpstreamBillingProbeExtraKey)
}

func TestAdminAccountEditPreservesRateSynchronizedAfterLoad(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	initialRate := 0.1
	account := mustCreateAccount(t, tx.Client(), &service.Account{
		Name:           "probe-rate-concurrent-edit",
		Platform:       service.PlatformOpenAI,
		Type:           service.AccountTypeAPIKey,
		RateMultiplier: &initialRate,
		Credentials:    map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			service.UpstreamBillingProbeEnabledExtraKey:    true,
			service.UpstreamBillingRateSyncEnabledExtraKey: true,
		},
	})

	staleAdminEdit, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	probeAccount, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)

	synchronizedRate := 0.2
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, probeAccount, &service.UpstreamBillingProbeSnapshot{
		Status:        service.UpstreamBillingProbeStatusOK,
		LastAttemptAt: time.Now().UTC(),
	}, &synchronizedRate))

	staleAdminEdit.Name = "name-only-edit"
	require.NoError(t, repo.UpdateWithAccountBillingSettings(ctx, staleAdminEdit, nil, nil, nil))

	got, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, "name-only-edit", got.Name)
	require.NotNil(t, got.RateMultiplier)
	require.Equal(t, synchronizedRate, *got.RateMultiplier)
}

func TestProbeSnapshotSyncsRateOnlyForSuccessfulEnabledAccount(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	initialRate := 0.25
	account := mustCreateAccount(t, tx.Client(), &service.Account{
		Name:           "probe-rate-sync",
		Platform:       service.PlatformGemini,
		Type:           service.AccountTypeAPIKey,
		RateMultiplier: &initialRate,
		Credentials:    map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			service.UpstreamBillingProbeEnabledExtraKey:    true,
			service.UpstreamBillingRateSyncEnabledExtraKey: true,
		},
	})

	loaded, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	syncedRate := 0.065
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, loaded, &service.UpstreamBillingProbeSnapshot{
		Status:        service.UpstreamBillingProbeStatusOK,
		LastAttemptAt: time.Now().UTC(),
	}, &syncedRate))

	got, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, got.RateMultiplier)
	require.Equal(t, syncedRate, *got.RateMultiplier)

	failedRate := 0.9
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, got, &service.UpstreamBillingProbeSnapshot{
		Status:        service.UpstreamBillingProbeStatusFailed,
		LastAttemptAt: time.Now().UTC(),
	}, &failedRate))
	got, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, got.RateMultiplier)
	require.Equal(t, syncedRate, *got.RateMultiplier)

	require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{
		service.UpstreamBillingRateSyncEnabledExtraKey: false,
	}))
	manual, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	manualProbeRate := 0.4
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, manual, &service.UpstreamBillingProbeSnapshot{
		Status:        service.UpstreamBillingProbeStatusOK,
		LastAttemptAt: time.Now().UTC(),
	}, &manualProbeRate))
	got, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, got.RateMultiplier)
	require.Equal(t, syncedRate, *got.RateMultiplier)

	require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{
		service.UpstreamBillingProbeEnabledExtraKey:    false,
		service.UpstreamBillingRateSyncEnabledExtraKey: false,
	}))
	disabled, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, disabled, &service.UpstreamBillingProbeSnapshot{
		Status:        service.UpstreamBillingProbeStatusOK,
		LastAttemptAt: time.Now().UTC(),
	}, &manualProbeRate))
	got, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, got.RateMultiplier)
	require.Equal(t, syncedRate, *got.RateMultiplier)
}

func TestAccountUpdatePreservesConcurrentProbeEnableFlag(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	account := mustCreateAccount(t, tx.Client(), &service.Account{
		Name:        "probe-update-enable",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			service.UpstreamBillingProbeEnabledExtraKey: true,
			service.UpstreamBillingProbeExtraKey:        map[string]any{"status": service.UpstreamBillingProbeStatusOK},
		},
	})

	stale, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{service.UpstreamBillingProbeEnabledExtraKey: false}))
	stale.Name = "ordinary-edit"
	require.NoError(t, repo.Update(ctx, stale))

	got, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, false, got.Extra[service.UpstreamBillingProbeEnabledExtraKey])
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
}

func TestAccountUpdateClearsProbeSnapshotWhenIdentityChanges(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	account := mustCreateAccount(t, tx.Client(), &service.Account{
		Name:        "probe-update-identity",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-old"},
		Extra: map[string]any{
			service.UpstreamBillingProbeEnabledExtraKey: true,
			service.UpstreamBillingProbeExtraKey:        map[string]any{"status": service.UpstreamBillingProbeStatusOK},
		},
	})

	loaded, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	loaded.Credentials["api_key"] = "sk-new"
	require.NoError(t, repo.Update(ctx, loaded))

	got, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
}

func TestBulkUpdateAndCredentialUpdateDeleteProbeKey(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	newAccount := func(name string) *service.Account {
		return mustCreateAccount(t, tx.Client(), &service.Account{
			Name:        name,
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk-old"},
			Extra: map[string]any{
				service.UpstreamBillingProbeEnabledExtraKey: true,
				service.UpstreamBillingProbeExtraKey:        map[string]any{"status": service.UpstreamBillingProbeStatusOK},
			},
		})
	}

	bulkAccount := newAccount("probe-bulk-clear")
	_, err := repo.BulkUpdate(ctx, []int64{bulkAccount.ID}, service.AccountBulkUpdate{
		Extra: map[string]any{service.UpstreamBillingProbeExtraKey: nil},
	})
	require.NoError(t, err)
	got, err := repo.GetByID(ctx, bulkAccount.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)

	credentialAccount := newAccount("probe-credentials-clear")
	require.NoError(t, repo.UpdateCredentials(ctx, credentialAccount.ID, map[string]any{"api_key": "sk-new"}))
	got, err = repo.GetByID(ctx, credentialAccount.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
}

func TestProbeSnapshotCASIncludesLoadedEnabledState(t *testing.T) {
	tests := []struct {
		name           string
		loadedEnabled  bool
		concurrentFlip *bool
		wantConflict   bool
	}{
		{name: "manual_false_stays_false", loadedEnabled: false},
		{name: "periodic_true_disabled_in_flight", loadedEnabled: true, concurrentFlip: boolPtr(false), wantConflict: true},
		{name: "manual_false_enabled_in_flight", loadedEnabled: false, concurrentFlip: boolPtr(true), wantConflict: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			tx := testEntTx(t)
			repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
			account := mustCreateAccount(t, tx.Client(), &service.Account{
				Name:        "probe-enabled-cas-" + tt.name,
				Platform:    service.PlatformOpenAI,
				Type:        service.AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "sk-test"},
				Extra:       map[string]any{service.UpstreamBillingProbeEnabledExtraKey: tt.loadedEnabled},
			})
			inFlight, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			if tt.concurrentFlip != nil {
				require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{
					service.UpstreamBillingProbeEnabledExtraKey: *tt.concurrentFlip,
				}))
			}

			err = repo.UpdateUpstreamBillingProbeSnapshot(ctx, inFlight, &service.UpstreamBillingProbeSnapshot{
				Status:        service.UpstreamBillingProbeStatusOK,
				LastAttemptAt: time.Now().UTC(),
			}, nil)
			if tt.wantConflict {
				require.ErrorIs(t, err, service.ErrUpstreamBillingProbeIdentityChanged)
			} else {
				require.NoError(t, err)
			}
			got, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			if tt.wantConflict {
				require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
			} else {
				require.Contains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
			}
		})
	}
}

func TestProbeSnapshotCASProtectsManualRateAfterSyncDisabled(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	initialRate := 0.25
	account := mustCreateAccount(t, tx.Client(), &service.Account{
		Name:           "probe-sync-cas",
		Platform:       service.PlatformAnthropic,
		Type:           service.AccountTypeAPIKey,
		RateMultiplier: &initialRate,
		Credentials:    map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			service.UpstreamBillingProbeEnabledExtraKey:    true,
			service.UpstreamBillingRateSyncEnabledExtraKey: true,
		},
	})

	inFlight, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	manual, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	manualRate := 0.8
	syncDisabled := false
	require.NoError(t, repo.UpdateWithAccountBillingSettings(ctx, manual, nil, &syncDisabled, &manualRate))

	probedRate := 0.1
	err = repo.UpdateUpstreamBillingProbeSnapshot(ctx, inFlight, &service.UpstreamBillingProbeSnapshot{
		Status:        service.UpstreamBillingProbeStatusOK,
		LastAttemptAt: time.Now().UTC(),
	}, &probedRate)
	require.ErrorIs(t, err, service.ErrUpstreamBillingProbeIdentityChanged)

	got, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, got.RateMultiplier)
	require.Equal(t, manualRate, *got.RateMultiplier)
	require.Equal(t, false, got.Extra[service.UpstreamBillingRateSyncEnabledExtraKey])
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
}

func boolPtr(value bool) *bool {
	return &value
}

func TestProxyIdentityUpdateInvalidatesProbeAndRejectsInFlightSnapshot(t *testing.T) {
	tests := []struct {
		name             string
		includeProbeKey  bool
		probeValue       any
		wantInvalidation bool
	}{
		{name: "missing_snapshot"},
		{name: "json_null_snapshot", includeProbeKey: true},
		{name: "existing_snapshot", includeProbeKey: true, probeValue: map[string]any{"status": service.UpstreamBillingProbeStatusOK}, wantInvalidation: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			tx := testEntTx(t)
			accountRepo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
			proxyRepo := newProxyRepositoryWithSQL(tx.Client(), tx)
			proxy := mustCreateProxy(t, tx.Client(), &service.Proxy{
				Name:     "probe-proxy",
				Protocol: "http",
				Host:     "old.example",
				Port:     8080,
				Username: "old-user",
				Password: "old-pass",
				Status:   service.StatusActive,
			})
			extra := map[string]any{service.UpstreamBillingProbeEnabledExtraKey: true}
			if tt.includeProbeKey {
				extra[service.UpstreamBillingProbeExtraKey] = tt.probeValue
			}
			account := mustCreateAccount(t, tx.Client(), &service.Account{
				Name:        "proxy-probe-account",
				Platform:    service.PlatformOpenAI,
				Type:        service.AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "sk-test"},
				Extra:       extra,
				ProxyID:     &proxy.ID,
			})
			inFlight, err := accountRepo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			require.NotNil(t, inFlight.Proxy)
			require.Equal(t, "old.example", inFlight.Proxy.Host)

			proxyToUpdate, err := proxyRepo.GetByID(ctx, proxy.ID)
			require.NoError(t, err)
			proxyToUpdate.Host = "new.example"
			require.NoError(t, proxyRepo.Update(ctx, proxyToUpdate))

			got, err := accountRepo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			if tt.wantInvalidation || !tt.includeProbeKey {
				require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
			} else {
				require.Contains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
				require.Nil(t, got.Extra[service.UpstreamBillingProbeExtraKey])
			}
			if !tt.wantInvalidation {
				require.Equal(t, inFlight.UpdatedAt, got.UpdatedAt, "missing/null snapshots must not cause an account row write")
			}
			err = accountRepo.UpdateUpstreamBillingProbeSnapshot(ctx, inFlight, &service.UpstreamBillingProbeSnapshot{
				Status:        service.UpstreamBillingProbeStatusOK,
				LastAttemptAt: time.Now().UTC(),
			}, nil)
			require.ErrorIs(t, err, service.ErrUpstreamBillingProbeIdentityChanged)

			rows, err := tx.QueryContext(ctx, `
				SELECT COUNT(*), COALESCE(MAX(payload::text), '')
				FROM scheduler_outbox
				WHERE event_type = $1
			`, service.SchedulerOutboxEventAccountBulkChanged)
			require.NoError(t, err)
			require.True(t, rows.Next())
			var (
				outboxCount int
				payloadJSON string
			)
			require.NoError(t, rows.Scan(&outboxCount, &payloadJSON))
			require.NoError(t, rows.Close())
			if tt.wantInvalidation {
				require.Equal(t, 1, outboxCount)
				var payload struct {
					AccountIDs []int64 `json:"account_ids"`
				}
				require.NoError(t, json.Unmarshal([]byte(payloadJSON), &payload))
				require.Equal(t, []int64{account.ID}, payload.AccountIDs)
			} else {
				require.Zero(t, outboxCount, "no snapshot change means no PR2 cache invalidation event")
			}
		})
	}
}

func TestSweepExpiredProxyWithoutFallbackInvalidatesOnlyExistingProbeSnapshot(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	proxyRepo := newProxyRepositoryWithSQL(tx.Client(), tx)
	accountRepo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	past := time.Now().Add(-time.Hour)
	proxy := &service.Proxy{
		Name:           "expired-probe-proxy-none",
		Protocol:       "http",
		Host:           "127.0.0.1",
		Port:           8080,
		Status:         service.StatusActive,
		ExpiresAt:      &past,
		FallbackMode:   service.FallbackModeNone,
		ExpiryWarnDays: 7,
	}
	require.NoError(t, proxyRepo.Create(ctx, proxy))
	newAccount := func(name string, probe any, includeProbe bool) *service.Account {
		extra := map[string]any{service.UpstreamBillingProbeEnabledExtraKey: true}
		if includeProbe {
			extra[service.UpstreamBillingProbeExtraKey] = probe
		}
		return mustCreateAccount(t, tx.Client(), &service.Account{
			Name:        name,
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk-test"},
			Extra:       extra,
			ProxyID:     &proxy.ID,
		})
	}
	withSnapshot := newAccount("expired-proxy-with-snapshot", map[string]any{"status": service.UpstreamBillingProbeStatusOK}, true)
	withoutSnapshot := newAccount("expired-proxy-without-snapshot", nil, false)
	withJSONNull := newAccount("expired-proxy-null-snapshot", nil, true)
	untouchedUpdatedAt := make(map[int64]time.Time, 2)
	for _, untouched := range []*service.Account{withoutSnapshot, withJSONNull} {
		loaded, err := accountRepo.GetByID(ctx, untouched.ID)
		require.NoError(t, err)
		untouchedUpdatedAt[untouched.ID] = loaded.UpdatedAt
	}

	changed, err := proxyRepo.SweepExpiredProxies(ctx, time.Now())
	require.NoError(t, err)
	require.Zero(t, changed, "probe invalidation must not inflate the rerouted account count")

	got, err := accountRepo.GetByID(ctx, withSnapshot.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
	for _, untouched := range []*service.Account{withoutSnapshot, withJSONNull} {
		got, err = accountRepo.GetByID(ctx, untouched.ID)
		require.NoError(t, err)
		require.Equal(t, untouchedUpdatedAt[untouched.ID], got.UpdatedAt)
	}

	payload := latestBulkAccountOutboxPayload(t, ctx, tx)
	require.Equal(t, []int64{withSnapshot.ID}, payload)
}

func TestSweepExpiredProxyFallbackRerouteDeletesProbeSnapshot(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	proxyRepo := newProxyRepositoryWithSQL(tx.Client(), tx)
	accountRepo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	past := time.Now().Add(-time.Hour)
	proxy := &service.Proxy{
		Name:           "expired-probe-proxy-direct",
		Protocol:       "http",
		Host:           "127.0.0.1",
		Port:           8080,
		Status:         service.StatusActive,
		ExpiresAt:      &past,
		FallbackMode:   service.FallbackModeDirect,
		ExpiryWarnDays: 7,
	}
	require.NoError(t, proxyRepo.Create(ctx, proxy))
	account := mustCreateAccount(t, tx.Client(), &service.Account{
		Name:        "expired-proxy-rerouted-snapshot",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			service.UpstreamBillingProbeEnabledExtraKey: true,
			service.UpstreamBillingProbeExtraKey:        map[string]any{"status": service.UpstreamBillingProbeStatusOK},
		},
		ProxyID: &proxy.ID,
	})

	changed, err := proxyRepo.SweepExpiredProxies(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, changed)

	got, err := accountRepo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Nil(t, got.ProxyID)
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
	require.Equal(t, []int64{account.ID}, latestBulkAccountOutboxPayload(t, ctx, tx))
}

func latestBulkAccountOutboxPayload(t *testing.T, ctx context.Context, tx sqlQueryer) []int64 {
	t.Helper()
	var payloadJSON []byte
	require.NoError(t, scanSingleRow(ctx, tx, `
		SELECT payload
		FROM scheduler_outbox
		WHERE event_type = $1
		ORDER BY id DESC
		LIMIT 1
	`, []any{service.SchedulerOutboxEventAccountBulkChanged}, &payloadJSON))
	var payload struct {
		AccountIDs []int64 `json:"account_ids"`
	}
	require.NoError(t, json.Unmarshal(payloadJSON, &payload))
	return payload.AccountIDs
}

func TestProbeSnapshotBalanceRoundTripsThroughCASAndIdentityClear(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	account := mustCreateAccount(t, tx.Client(), &service.Account{
		Name:        "probe-balance-round-trip",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-old", "base_url": "https://relay.example/v1"},
		Extra:       map[string]any{service.UpstreamBillingProbeEnabledExtraKey: true},
	})
	receivedAt := time.Date(2026, time.July, 13, 1, 0, 0, 0, time.UTC)
	freshUntil := receivedAt.Add(time.Hour)
	snapshotWithBalance := func(remaining float64) *service.UpstreamBillingProbeSnapshot {
		return &service.UpstreamBillingProbeSnapshot{
			Status:        service.UpstreamBillingProbeStatusUnsupported,
			LastAttemptAt: receivedAt,
			NextProbeAt:   receivedAt.Add(30 * time.Minute),
			Balance: &service.UpstreamBalanceSnapshot{
				Status: service.UpstreamBillingProbeStatusOK,
				Data: map[string]any{
					"is_valid":  true,
					"plan_name": "钱包余额",
					"remaining": remaining,
					"windows":   []map[string]any{{"window": "5h", "limit": 5.0, "used": 1.25}},
				},
				ReceivedAt:    &receivedAt,
				FreshUntil:    &freshUntil,
				LastAttemptAt: receivedAt,
			},
		}
	}

	loaded, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, loaded, snapshotWithBalance(1084.44), nil))

	// The next probe's CAS compares against the snapshot as read back from
	// JSONB, nested balance included.
	reloaded, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	items := service.BuildUpstreamBillingRateSnapshotItems([]service.Account{*reloaded})
	require.Len(t, items, 1)
	require.NotNil(t, items[0].Snapshot)
	require.NotNil(t, items[0].Snapshot.Balance)
	require.Equal(t, 1084.44, items[0].Snapshot.Balance.Data["remaining"])
	require.Equal(t, []any{map[string]any{"window": "5h", "limit": 5.0, "used": 1.25}}, items[0].Snapshot.Balance.Data["windows"])
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, reloaded, snapshotWithBalance(12.5), nil))

	updated, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	raw, err := json.Marshal(updated.Extra[service.UpstreamBillingProbeExtraKey])
	require.NoError(t, err)
	require.Contains(t, string(raw), `"remaining":12.5`)

	updated.Credentials["api_key"] = "sk-new"
	require.NoError(t, repo.Update(ctx, updated))
	cleared, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotContains(t, cleared.Extra, service.UpstreamBillingProbeExtraKey)
}

func TestProbeReplacesSavedCostMultiplier(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	now := time.Now().UTC()
	for _, tt := range []struct {
		name        string
		status      string
		accountType string
		rate        float64
		want        float64
	}{
		{"success without billing sync", service.UpstreamBillingProbeStatusOK, service.AccountTypeAPIKey, 0.14, 0.14},
		{"zero upstream", service.UpstreamBillingProbeStatusOK, service.AccountTypeAPIKey, 0, 0},
		{"failed cached result", service.UpstreamBillingProbeStatusFailed, service.AccountTypeAPIKey, 0.14, 0.1},
		{"unsupported cached result", service.UpstreamBillingProbeStatusUnsupported, service.AccountTypeAPIKey, 0.14, 0.1},
		{"out of range", service.UpstreamBillingProbeStatusOK, service.AccountTypeAPIKey, 1000001, 0.1},
		{"oauth", service.UpstreamBillingProbeStatusOK, service.AccountTypeOAuth, 0.14, 0.1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			billing := 7.0
			account := mustCreateAccount(t, tx.Client(), &service.Account{
				Name: "cost-sync-" + tt.name, Platform: service.PlatformOpenAI, Type: tt.accountType,
				RateMultiplier: &billing, Credentials: map[string]any{"api_key": "sk-test"},
				Extra: map[string]any{service.AccountCostMultiplierExtraKey: 0.1, "unrelated": "keep"},
			})
			require.NoError(t, tx.Client().Account.UpdateOneID(account.ID).SetRateMultiplier(billing).Exec(ctx))
			loaded, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			snapshot := &service.UpstreamBillingProbeSnapshot{Status: tt.status, LastAttemptAt: now, Data: map[string]any{
				"billing_scope": "token", "resolved_rate_multiplier": tt.rate, "peak_rate_enabled": false,
			}}
			require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, loaded, snapshot, nil))
			got, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			require.Equal(t, tt.want, got.Extra[service.AccountCostMultiplierExtraKey])
			require.Equal(t, tt.want, got.CostMultiplier())
			require.Equal(t, billing, got.BillingRateMultiplier())
			require.Equal(t, "keep", got.Extra["unrelated"])
			items := service.BuildUpstreamBillingRateSnapshotItems([]service.Account{*got})
			require.Equal(t, tt.want, items[0].CostMultiplier, "background refresh returns the saved cost")
			// A name-only save from a form opened before the probe must preserve cost.
			delete(loaded.Extra, service.AccountCostMultiplierExtraKey)
			loaded.Name = "renamed-" + tt.name
			require.NoError(t, repo.Update(ctx, loaded))
			renamed, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			require.Equal(t, tt.want, renamed.CostMultiplier())
			snapshot.Status = service.UpstreamBillingProbeStatusFailed
			require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, got, snapshot, nil))
			afterFailure, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			require.Equal(t, tt.want, afterFailure.CostMultiplier(), "failed probes preserve the saved value")
		})
	}
}

func TestProbePreservesManualCostWhenAutoSyncDisabledDuringProbe(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	account := mustCreateAccount(t, tx.Client(), &service.Account{
		Name: "manual-cost", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra:       map[string]any{service.AccountCostMultiplierExtraKey: 0.1},
	})
	inFlight, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.True(t, inFlight.CostMultiplierAutoSyncEnabled())
	// A 5x recharge credit means the operator's actual cost is 0.2, even though
	// the upstream reports 1. Complete an already-running probe after saving it.
	require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{
		service.AccountCostMultiplierExtraKey: 0.2, service.AccountCostAutoSyncExtraKey: false,
	}))
	snapshot := &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK, LastAttemptAt: time.Now().UTC(), Data: map[string]any{
		"billing_scope": "token", "resolved_rate_multiplier": 1.0, "peak_rate_enabled": false,
	}}
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, inFlight, snapshot, nil))
	manual, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, 0.2, manual.CostMultiplier())
	require.False(t, manual.CostMultiplierAutoSyncEnabled())
	items := service.BuildUpstreamBillingRateSnapshotItems([]service.Account{*manual})
	require.Equal(t, 0.2, items[0].CostMultiplier)
	require.Equal(t, 1.0, items[0].Snapshot.Data["resolved_rate_multiplier"], "upstream probing still updates its snapshot")
	// An unrelated edit from an older client must not clear either setting.
	delete(inFlight.Extra, service.AccountCostMultiplierExtraKey)
	inFlight.Name = "renamed"
	require.NoError(t, repo.Update(ctx, inFlight))
	renamed, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, 0.2, renamed.CostMultiplier())
	require.False(t, renamed.CostMultiplierAutoSyncEnabled())
	// Subsequent manual probes remain read-only with respect to cost.
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, renamed, snapshot, nil))
	manual, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, 0.2, manual.CostMultiplier())
	// Re-enabling only changes the mode; the next successful probe updates cost.
	require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{service.AccountCostAutoSyncExtraKey: true}))
	enabled, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, 0.2, enabled.CostMultiplier())
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, enabled, snapshot, nil))
	synced, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, 1.0, synced.CostMultiplier())
}

func TestAccountProbePenaltyOmittedEditPreservesCommittedSetting(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	account := mustCreateAccount(t, tx.Client(), &service.Account{Name: "probe-penalty-omit", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Extra: map[string]any{service.AccountProbePenaltyExtraKey: map[string]any{"enabled": true, "minimum_charge": .001}}})
	stale, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{service.AccountProbePenaltyExtraKey: map[string]any{"enabled": false, "minimum_charge": .002}}))
	delete(stale.Extra, service.AccountProbePenaltyExtraKey)
	stale.Name = "unrelated rename"
	require.NoError(t, repo.Update(ctx, stale))
	saved, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"enabled": false, "minimum_charge": .002}, saved.Extra[service.AccountProbePenaltyExtraKey])
	stale.Extra[service.AccountProbePenaltyExtraKey] = map[string]any{"enabled": true, "minimum_charge": .003}
	require.NoError(t, repo.Update(ctx, stale))
	saved, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, .003, saved.ProbePenaltyMinimum())
	stale.Extra[service.AccountProbePenaltyExtraKey] = nil
	require.NoError(t, repo.Update(ctx, stale))
	saved, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Zero(t, saved.ProbePenaltyMinimum())
}
