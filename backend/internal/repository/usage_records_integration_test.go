//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUsageRecordsKeysetIsolation(t *testing.T) {
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	ctx := context.Background()
	start := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	owner := mustCreateUser(t, client, &service.User{Email: "records-owner@example.test"})
	other := mustCreateUser(t, client, &service.User{Email: "records-other@example.test"})
	account := mustCreateAccount(t, client, &service.Account{Name: "usage-records"})
	key := func(uid int64) int64 {
		return mustCreateApiKey(t, client, &service.APIKey{UserID: uid, Name: "records", Key: uuid.NewString()}).ID
	}
	keyID := key(owner.ID)
	insert := func(uid, kid int64, at time.Time) int64 {
		log := &service.UsageLog{UserID: uid, APIKeyID: kid, AccountID: account.ID, RequestID: uuid.NewString(), Model: "example", CreatedAt: at, ActualCost: 0.01}
		inserted, err := repo.Create(ctx, log)
		require.NoError(t, err)
		require.True(t, inserted)
		return log.ID
	}
	insert(owner.ID, key(owner.ID), start)
	insert(other.ID, key(other.ID), start)
	insert(other.ID, keyID, start) // Malformed historical attribution must also be excluded.
	insert(owner.ID, keyID, start.Add(-time.Microsecond))
	stamp := start.Add(time.Minute)
	first := insert(owner.ID, keyID, stamp)
	second := insert(owner.ID, keyID, stamp)
	lateTime := end.Add(-time.Microsecond)
	last := insert(owner.ID, keyID, lateTime)
	insert(owner.ID, keyID, end)
	ids := func(logs []service.UsageLog) []int64 {
		var out []int64
		for _, l := range logs {
			out = append(out, l.ID)
		}
		return out
	}
	page, err := repo.ListUsageRecords(ctx, owner.ID, keyID, start, end, nil, 1)
	require.NoError(t, err)
	require.Equal(t, []int64{first, second}, ids(page))
	added := insert(owner.ID, keyID, stamp)
	lateEarlier := insert(owner.ID, keyID, start) // Restored after page one, before its cursor.
	_, err = tx.ExecContext(ctx, "UPDATE usage_logs SET image_count = NULL, requested_model = $1 WHERE id = $2", " \t ", lateEarlier)
	require.NoError(t, err)
	pos := &service.UsageRecordPosition{CreatedAt: page[0].CreatedAt, ID: page[0].ID}
	page, err = repo.ListUsageRecords(ctx, owner.ID, keyID, start, end, pos, 2)
	require.NoError(t, err)
	require.Equal(t, []int64{second, added, last}, ids(page))
	pos = &service.UsageRecordPosition{CreatedAt: page[1].CreatedAt, ID: page[1].ID}
	page, err = repo.ListUsageRecords(ctx, owner.ID, keyID, start, end, pos, 2)
	require.NoError(t, err)
	require.Equal(t, []int64{last}, ids(page))
	page, err = repo.ListUsageRecords(ctx, owner.ID, keyID, start, end, nil, 10)
	require.NoError(t, err)
	require.Equal(t, []int64{lateEarlier, first, second, added, last}, ids(page))
	require.Zero(t, page[0].ImageCount)
	require.Equal(t, "example", page[0].RequestedModel)
	page, err = repo.ListUsageRecords(ctx, owner.ID, keyID, stamp.Add(time.Nanosecond), lateTime.Add(time.Nanosecond), nil, 10)
	require.NoError(t, err)
	require.Equal(t, []int64{last}, ids(page))
}
