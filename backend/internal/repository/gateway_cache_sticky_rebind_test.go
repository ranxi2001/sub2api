package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func stickyRebindTestSwap(accountID int64) service.SessionAccountBindingSwap {
	return service.SessionAccountBindingSwap{
		SessionHash:       "openai:current-session",
		LegacySessionHash: "openai:legacy-session",
		ExpectedAccountID: 101,
		AccountID:         accountID,
		TTL:               time.Hour,
		LegacyTTL:         10 * time.Minute,
		ReadLegacy:        true,
		WriteLegacy:       true,
	}
}

func TestStickySessionRebindPublishesBothFormatsAndPreservesGroupIsolation(t *testing.T) {
	cache, mr := newHTTPResponseOwnerTestCache(t)
	ctx := context.Background()
	swap := stickyRebindTestSwap(202)
	primary := buildSessionKey(47, swap.SessionHash)
	legacy := buildSessionKey(47, swap.LegacySessionHash)
	otherGroup := buildSessionKey(48, swap.SessionHash)
	require.NoError(t, cache.SetSessionAccountID(ctx, 47, swap.SessionHash, 101, time.Minute))
	require.NoError(t, cache.SetSessionAccountID(ctx, 47, swap.LegacySessionHash, 101, time.Minute))
	require.NoError(t, cache.SetSessionAccountID(ctx, 48, swap.SessionHash, 909, 2*time.Minute))

	changed, err := cache.CompareAndSwapSessionAccountID(ctx, 47, swap)
	require.NoError(t, err)
	require.True(t, changed)
	value, err := mr.Get(primary)
	require.NoError(t, err)
	require.Equal(t, "202", value)
	value, err = mr.Get(legacy)
	require.NoError(t, err)
	require.Equal(t, "202", value, "an old reader must follow the admitted replacement")
	require.Equal(t, time.Hour, mr.TTL(primary))
	require.Equal(t, 10*time.Minute, mr.TTL(legacy))
	value, err = mr.Get(otherGroup)
	require.NoError(t, err)
	require.Equal(t, "909", value)
	require.Equal(t, 2*time.Minute, mr.TTL(otherGroup))
}

func TestStickySessionRebindStaleWaiterCannotUndoNewBinding(t *testing.T) {
	cache, mr := newHTTPResponseOwnerTestCache(t)
	ctx := context.Background()
	swap := stickyRebindTestSwap(101)
	primary := buildSessionKey(47, swap.SessionHash)
	legacy := buildSessionKey(47, swap.LegacySessionHash)
	// An older WaitPlan still owns 101, but a newer request has moved to 202.
	require.NoError(t, cache.SetSessionAccountID(ctx, 47, swap.SessionHash, 202, 2*time.Minute))
	require.NoError(t, cache.SetSessionAccountID(ctx, 47, swap.LegacySessionHash, 202, time.Minute))
	changed, err := cache.CompareAndSwapSessionAccountID(ctx, 47, swap)
	require.NoError(t, err)
	require.False(t, changed)
	for _, key := range []string{primary, legacy} {
		value, getErr := mr.Get(key)
		require.NoError(t, getErr)
		require.Equal(t, "202", value)
	}
	require.Equal(t, 2*time.Minute, mr.TTL(primary), "conflicts must not extend the winner's TTL")
	require.Equal(t, time.Minute, mr.TTL(legacy))
}

func TestStickySessionRebindDistinguishesLargeAccountIDs(t *testing.T) {
	cache, mr := newHTTPResponseOwnerTestCache(t)
	ctx := context.Background()
	swap := stickyRebindTestSwap(202)
	// Adjacent integers above 2^53 compare equal when Lua coerces them to doubles.
	swap.ExpectedAccountID = 9007199254740992
	const currentAccountID int64 = 9007199254740993
	for _, session := range []string{swap.SessionHash, swap.LegacySessionHash} {
		require.NoError(t, cache.SetSessionAccountID(ctx, 47, session, currentAccountID, time.Minute))
	}
	changed, err := cache.CompareAndSwapSessionAccountID(ctx, 47, swap)
	require.NoError(t, err)
	require.False(t, changed)
	for _, session := range []string{swap.SessionHash, swap.LegacySessionHash} {
		accountID, getErr := cache.GetSessionAccountID(ctx, 47, session)
		require.NoError(t, getErr)
		require.Equal(t, currentAccountID, accountID)
		require.Equal(t, time.Minute, mr.TTL(buildSessionKey(47, session)))
	}
}

func TestStickySessionRebindPrimaryTakesPrecedenceOverLegacy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		primary int64
		legacy  int64
		changed bool
	}{
		{name: "primary matches despite different legacy", primary: 101, legacy: 303, changed: true},
		{name: "legacy match cannot override newer primary", primary: 303, legacy: 101, changed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache, mr := newHTTPResponseOwnerTestCache(t)
			ctx := context.Background()
			swap := stickyRebindTestSwap(202)
			require.NoError(t, cache.SetSessionAccountID(ctx, 47, swap.SessionHash, tc.primary, time.Minute))
			require.NoError(t, cache.SetSessionAccountID(ctx, 47, swap.LegacySessionHash, tc.legacy, time.Minute))
			changed, err := cache.CompareAndSwapSessionAccountID(ctx, 47, swap)
			require.NoError(t, err)
			require.Equal(t, tc.changed, changed)
			primary, err := cache.GetSessionAccountID(ctx, 47, swap.SessionHash)
			require.NoError(t, err)
			legacy, err := cache.GetSessionAccountID(ctx, 47, swap.LegacySessionHash)
			require.NoError(t, err)
			if tc.changed {
				require.EqualValues(t, 202, primary)
				require.EqualValues(t, 202, legacy)
			} else {
				require.Equal(t, tc.primary, primary)
				require.Equal(t, tc.legacy, legacy)
				require.Equal(t, time.Minute, mr.TTL(buildSessionKey(47, swap.SessionHash)))
				require.Equal(t, time.Minute, mr.TTL(buildSessionKey(47, swap.LegacySessionHash)))
			}
		})
	}
}

func TestStickySessionRebindHonorsLegacyReadAndWriteFlags(t *testing.T) {
	for _, tc := range []struct {
		name        string
		legacy      int64
		readLegacy  bool
		writeLegacy bool
		changed     bool
	}{
		{name: "legacy-only binding can migrate", legacy: 101, readLegacy: true, writeLegacy: true, changed: true},
		{name: "legacy-only conflict blocks migration", legacy: 303, readLegacy: true, writeLegacy: true, changed: false},
		{name: "disabled fallback ignores stale legacy", legacy: 303, readLegacy: false, writeLegacy: false, changed: true},
		{name: "read-only legacy is removed after migration", legacy: 101, readLegacy: true, writeLegacy: false, changed: true},
		{name: "dual-write is independent of fallback", legacy: 303, readLegacy: false, writeLegacy: true, changed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache, mr := newHTTPResponseOwnerTestCache(t)
			ctx := context.Background()
			swap := stickyRebindTestSwap(202)
			swap.ReadLegacy, swap.WriteLegacy = tc.readLegacy, tc.writeLegacy
			require.NoError(t, cache.SetSessionAccountID(ctx, 47, swap.LegacySessionHash, tc.legacy, time.Minute))
			changed, err := cache.CompareAndSwapSessionAccountID(ctx, 47, swap)
			require.NoError(t, err)
			require.Equal(t, tc.changed, changed)
			primary := buildSessionKey(47, swap.SessionHash)
			legacy := buildSessionKey(47, swap.LegacySessionHash)
			if !tc.changed {
				require.False(t, mr.Exists(primary), "a fallback conflict must leave primary absent")
				value, getErr := mr.Get(legacy)
				require.NoError(t, getErr)
				require.Equal(t, "303", value)
				require.Equal(t, time.Minute, mr.TTL(legacy))
				return
			}
			value, getErr := mr.Get(primary)
			require.NoError(t, getErr)
			require.Equal(t, "202", value)
			if tc.writeLegacy {
				value, getErr = mr.Get(legacy)
				require.NoError(t, getErr)
				require.Equal(t, "202", value)
			} else {
				require.False(t, mr.Exists(legacy), "old fallback must not resurrect the previous account")
			}
		})
	}
}

func TestStickySessionRebindCompetingInstancesAgreeOnOneReplacement(t *testing.T) {
	cache, mr := newHTTPResponseOwnerTestCache(t)
	otherClient := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = otherClient.Close() })
	other := &gatewayCache{rdb: otherClient}
	ctx := context.Background()
	initial := stickyRebindTestSwap(202)
	require.NoError(t, cache.SetSessionAccountID(ctx, 47, initial.SessionHash, 101, time.Minute))
	require.NoError(t, cache.SetSessionAccountID(ctx, 47, initial.LegacySessionHash, 101, time.Minute))
	type result struct {
		accountID int64
		changed   bool
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for _, instance := range []struct {
		cache     *gatewayCache
		accountID int64
	}{{cache, 202}, {other, 303}} {
		workers.Add(1)
		go func(c *gatewayCache, accountID int64) {
			defer workers.Done()
			<-start
			changed, err := c.CompareAndSwapSessionAccountID(ctx, 47, stickyRebindTestSwap(accountID))
			results <- result{accountID: accountID, changed: changed, err: err}
		}(instance.cache, instance.accountID)
	}
	close(start)
	workers.Wait()
	close(results)
	var winners []int64
	for result := range results {
		require.NoError(t, result.err)
		if result.changed {
			winners = append(winners, result.accountID)
		}
	}
	require.Len(t, winners, 1, "both instances observed 101, so only one different target may publish")
	for _, session := range []string{initial.SessionHash, initial.LegacySessionHash} {
		accountID, err := cache.GetSessionAccountID(ctx, 47, session)
		require.NoError(t, err)
		require.Equal(t, winners[0], accountID, "both reader formats must agree on the winning migration")
	}
}

func TestStickySessionRebindMissingExpiredAndSameTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		seed   int64
		expire bool
	}{
		{name: "missing"},
		{name: "expired", seed: 101, expire: true},
		{name: "already rebound by another instance", seed: 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache, mr := newHTTPResponseOwnerTestCache(t)
			ctx := context.Background()
			swap := stickyRebindTestSwap(202)
			if tc.seed > 0 {
				for _, session := range []string{swap.SessionHash, swap.LegacySessionHash} {
					require.NoError(t, cache.SetSessionAccountID(ctx, 47, session, tc.seed, time.Second))
				}
			}
			if tc.expire {
				mr.FastForward(2 * time.Second)
			}
			changed, err := cache.CompareAndSwapSessionAccountID(ctx, 47, swap)
			require.NoError(t, err)
			require.True(t, changed)
			for _, session := range []string{swap.SessionHash, swap.LegacySessionHash} {
				accountID, getErr := cache.GetSessionAccountID(ctx, 47, session)
				require.NoError(t, getErr)
				require.EqualValues(t, 202, accountID)
			}
			require.Equal(t, time.Hour, mr.TTL(buildSessionKey(47, swap.SessionHash)))
			require.Equal(t, 10*time.Minute, mr.TTL(buildSessionKey(47, swap.LegacySessionHash)))
		})
	}
}

func TestStickySessionRebindRedisErrorsDoNotPublish(t *testing.T) {
	cache, mr := newHTTPResponseOwnerTestCache(t)
	ctx := context.Background()
	swap := stickyRebindTestSwap(202)
	require.NoError(t, cache.SetSessionAccountID(ctx, 47, swap.SessionHash, 101, time.Minute))
	require.NoError(t, cache.SetSessionAccountID(ctx, 47, swap.LegacySessionHash, 101, time.Minute))
	mr.SetError("synthetic-cache-outage")
	changed, err := cache.CompareAndSwapSessionAccountID(ctx, 47, swap)
	require.Error(t, err)
	require.False(t, changed)
	mr.SetError("")
	for _, session := range []string{swap.SessionHash, swap.LegacySessionHash} {
		accountID, getErr := cache.GetSessionAccountID(ctx, 47, session)
		require.NoError(t, getErr)
		require.EqualValues(t, 101, accountID, "a cache error must not trigger an unconditional SET fallback")
		require.Equal(t, time.Minute, mr.TTL(buildSessionKey(47, session)))
	}
}

func TestStickySessionRebindWithoutLegacyHashRetainsPrimaryTTL(t *testing.T) {
	cache, mr := newHTTPResponseOwnerTestCache(t)
	ctx := context.Background()
	swap := stickyRebindTestSwap(202)
	swap.LegacySessionHash = ""
	require.NoError(t, cache.SetSessionAccountID(ctx, 47, swap.SessionHash, 101, time.Minute))
	changed, err := cache.CompareAndSwapSessionAccountID(ctx, 47, swap)
	require.NoError(t, err)
	require.True(t, changed)
	accountID, err := cache.GetSessionAccountID(ctx, 47, swap.SessionHash)
	require.NoError(t, err)
	require.EqualValues(t, 202, accountID)
	require.Equal(t, time.Hour, mr.TTL(buildSessionKey(47, swap.SessionHash)))
	require.Len(t, mr.Keys(), 1, "an absent legacy hash must not create an empty compatibility key")
}

func TestStickySessionRebindInvalidExpiryDoesNotPartiallyPublish(t *testing.T) {
	for _, primaryExpiry := range []bool{true, false} {
		name := "legacy"
		if primaryExpiry {
			name = "primary"
		}
		t.Run(name, func(t *testing.T) {
			cache, mr := newHTTPResponseOwnerTestCache(t)
			ctx := context.Background()
			swap := stickyRebindTestSwap(202)
			for _, session := range []string{swap.SessionHash, swap.LegacySessionHash} {
				require.NoError(t, cache.SetSessionAccountID(ctx, 47, session, 101, time.Minute))
			}
			if primaryExpiry {
				swap.TTL = 0
			} else {
				swap.LegacyTTL = 0
			}
			changed, err := cache.CompareAndSwapSessionAccountID(ctx, 47, swap)
			require.Error(t, err)
			require.False(t, changed)
			for _, session := range []string{swap.SessionHash, swap.LegacySessionHash} {
				accountID, getErr := cache.GetSessionAccountID(ctx, 47, session)
				require.NoError(t, getErr)
				require.EqualValues(t, 101, accountID, "Lua runtime errors cannot roll back an earlier SET")
				require.Equal(t, time.Minute, mr.TTL(buildSessionKey(47, session)))
			}
		})
	}
}

func TestStickySessionRebindWrongTypeDoesNotPartiallyPublish(t *testing.T) {
	for _, corruptPrimary := range []bool{true, false} {
		name := "legacy fallback"
		if corruptPrimary {
			name = "primary"
		}
		t.Run(name, func(t *testing.T) {
			cache, mr := newHTTPResponseOwnerTestCache(t)
			ctx := context.Background()
			swap := stickyRebindTestSwap(202)
			primary := buildSessionKey(47, swap.SessionHash)
			legacy := buildSessionKey(47, swap.LegacySessionHash)
			badKey := legacy
			if corruptPrimary {
				badKey = primary
				require.NoError(t, cache.SetSessionAccountID(ctx, 47, swap.LegacySessionHash, 101, time.Minute))
			}
			mr.HSet(badKey, "unexpected", "type")
			changed, err := cache.CompareAndSwapSessionAccountID(ctx, 47, swap)
			require.Error(t, err)
			require.False(t, changed)
			require.Equal(t, "hash", mr.Type(badKey))
			if corruptPrimary {
				value, getErr := mr.Get(legacy)
				require.NoError(t, getErr)
				require.Equal(t, "101", value)
			} else {
				require.False(t, mr.Exists(primary), "a failing fallback read must happen before the primary SET")
			}
		})
	}
}

func TestStickySessionRebindUnusedLegacyTypeDoesNotBlockPrimary(t *testing.T) {
	for _, tc := range []struct {
		name           string
		primaryPresent bool
		readLegacy     bool
		writeLegacy    bool
	}{
		{name: "primary wins and repairs old format", primaryPresent: true, readLegacy: true, writeLegacy: true},
		{name: "primary wins and removes disabled format", primaryPresent: true, readLegacy: true, writeLegacy: false},
		{name: "disabled fallback ignores the malformed old format", primaryPresent: false, readLegacy: false, writeLegacy: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache, mr := newHTTPResponseOwnerTestCache(t)
			ctx := context.Background()
			swap := stickyRebindTestSwap(202)
			swap.ReadLegacy, swap.WriteLegacy = tc.readLegacy, tc.writeLegacy
			if tc.primaryPresent {
				require.NoError(t, cache.SetSessionAccountID(ctx, 47, swap.SessionHash, 101, time.Minute))
			}
			legacy := buildSessionKey(47, swap.LegacySessionHash)
			mr.HSet(legacy, "unexpected", "type")
			changed, err := cache.CompareAndSwapSessionAccountID(ctx, 47, swap)
			require.NoError(t, err)
			require.True(t, changed)
			accountID, err := cache.GetSessionAccountID(ctx, 47, swap.SessionHash)
			require.NoError(t, err)
			require.EqualValues(t, 202, accountID)
			if tc.writeLegacy {
				accountID, err = cache.GetSessionAccountID(ctx, 47, swap.LegacySessionHash)
				require.NoError(t, err)
				require.EqualValues(t, 202, accountID)
			} else {
				require.False(t, mr.Exists(legacy))
			}
		})
	}
}
