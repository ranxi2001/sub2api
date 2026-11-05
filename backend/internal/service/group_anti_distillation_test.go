package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type antiDistillationSettings struct {
	SettingRepository
	mu     sync.Mutex
	values map[string]string
	err    error
}

func (r *antiDistillationSettings) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return "", r.err
	}
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *antiDistillationSettings) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.values[key] = value
	return nil
}

type antiDistillationGroups struct {
	GroupRepository
	groups map[int64]*Group
}

func (r *antiDistillationGroups) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	if group := r.groups[id]; group != nil {
		return group, nil
	}
	return nil, ErrGroupNotFound
}

func antiDistillationTestService(t *testing.T) (*GroupAntiDistillationService, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	settings := &antiDistillationSettings{values: make(map[string]string)}
	groups := &antiDistillationGroups{groups: map[int64]*Group{
		1: {ID: 1, Platform: PlatformOpenAI},
		2: {ID: 2, Platform: PlatformOpenAI},
		3: {ID: 3, Platform: PlatformAnthropic},
	}}
	return NewGroupAntiDistillationService(settings, groups, client, nil, nil), server
}

func TestGroupAntiDistillationConfigurationDefaultsAndValidation(t *testing.T) {
	svc, _ := antiDistillationTestService(t)
	ctx := context.Background()
	cfg, err := svc.GetConfig(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, DefaultGroupAntiDistillationConfig(), *cfg)
	require.NoError(t, svc.SaveConfig(ctx, 1, GroupAntiDistillationConfig{Enabled: true, PauseMinutes: 5}))
	cfg, err = svc.GetConfig(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 5, cfg.PauseMinutes)
	require.True(t, cfg.Enabled)

	for _, minutes := range []int{-1, 0, 1441} {
		require.Error(t, svc.SaveConfig(ctx, 1, GroupAntiDistillationConfig{PauseMinutes: minutes}))
	}
	for _, minutes := range []int{1, 1440} {
		require.NoError(t, svc.SaveConfig(ctx, 1, GroupAntiDistillationConfig{PauseMinutes: minutes}))
	}
	require.Error(t, svc.SaveConfig(ctx, 3, GroupAntiDistillationConfig{Enabled: true, PauseMinutes: 5}))
	require.ErrorIs(t, svc.SaveConfig(ctx, 404, GroupAntiDistillationConfig{PauseMinutes: 5}), ErrGroupNotFound)

	// Returned values cannot mutate a cached setting without saving.
	cfg, err = svc.GetConfig(ctx, 1)
	require.NoError(t, err)
	cfg.Enabled = true
	stored, err := svc.GetConfig(ctx, 1)
	require.NoError(t, err)
	require.False(t, stored.Enabled)
}

func TestGroupAntiDistillationTwentySamplesEighteenZerosAcrossKeysAndGroups(t *testing.T) {
	svc, _ := antiDistillationTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.SaveConfig(ctx, 1, GroupAntiDistillationConfig{Enabled: true, PauseMinutes: 5}))
	require.NoError(t, svc.SaveConfig(ctx, 2, GroupAntiDistillationConfig{Enabled: true, PauseMinutes: 10}))
	for i := 0; i < 19; i++ {
		groupID := int64(i%2 + 1)
		cache := 0
		if i < 2 {
			cache = 1
		}
		svc.Observe(ctx, &APIKey{ID: int64(i + 100), UserID: 7, Group: &Group{ID: groupID, Platform: PlatformOpenAI}}, fmt.Sprint(i), cache)
	}
	remaining, err := svc.Check(ctx, 7, 1)
	require.NoError(t, err)
	require.Zero(t, remaining, "19 samples cannot trigger")
	svc.Observe(ctx, &APIKey{ID: 999, UserID: 7, Group: &Group{ID: 2, Platform: PlatformOpenAI}}, "last", 0)
	remaining, err = svc.Check(ctx, 7, 1)
	require.NoError(t, err)
	require.InDelta(t, (10 * time.Minute).Milliseconds(), remaining.Milliseconds(), 1000, "triggering group determines duration")
	remaining, err = svc.Check(ctx, 8, 1)
	require.NoError(t, err)
	require.Zero(t, remaining, "another user is isolated")

	require.NoError(t, svc.SaveConfig(ctx, 1, GroupAntiDistillationConfig{PauseMinutes: 5}))
	remaining, err = svc.Check(ctx, 7, 1)
	require.NoError(t, err)
	require.Zero(t, remaining, "disabled groups ignore an active shared pause")
	remaining, err = svc.Check(ctx, 7, 2)
	require.NoError(t, err)
	require.Positive(t, remaining, "other enabled groups still enforce it")
}

func TestGroupAntiDistillationWindowSlidesAndDeduplicates(t *testing.T) {
	svc, _ := antiDistillationTestService(t)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		cache := 0
		if i < 3 {
			cache = 100
		}
		triggered, _, err := svc.observe(ctx, 7, fmt.Sprint(i), cache, 5*time.Minute)
		require.NoError(t, err)
		require.False(t, triggered, "17/20 must not trigger")
	}
	triggered, _, err := svc.observe(ctx, 7, "19", 0, 5*time.Minute)
	require.NoError(t, err)
	require.False(t, triggered, "duplicate final result must not replace a positive sample")
	triggered, _, err = svc.observe(ctx, 7, "20", 0, 5*time.Minute)
	require.NoError(t, err)
	require.True(t, triggered, "oldest positive sample slides out to yield 18/20")
}

func TestGroupAntiDistillationPauseNotRenewedAndOldInflightIgnored(t *testing.T) {
	svc, server := antiDistillationTestService(t)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		_, _, err := svc.observe(ctx, 7, fmt.Sprint(i), 0, time.Minute)
		require.NoError(t, err)
	}
	keys := groupAntiDistillationRedisKeys(7)
	firstTTL := server.TTL(keys[2])
	for i := 0; i < 30; i++ {
		triggered, _, err := svc.observe(ctx, 7, fmt.Sprint(20+i), 0, 10*time.Minute)
		require.NoError(t, err)
		require.False(t, triggered)
	}
	require.Equal(t, firstTTL, server.TTL(keys[2]), "late completions do not renew an established pause")
	require.False(t, server.Exists(keys[0]), "samples during a pause do not accumulate")

	lastUntil, err := svc.redis.Get(ctx, keys[3]).Int64()
	require.NoError(t, err)
	server.FastForward(time.Minute + time.Second)
	for i := 0; i < 30; i++ {
		oldCtx := WithGroupAntiDistillationRequestStart(ctx, time.UnixMilli(lastUntil-1))
		triggered, _, err := svc.observe(oldCtx, 7, fmt.Sprint(100+i), 0, time.Minute)
		require.NoError(t, err)
		require.False(t, triggered)
	}
	require.False(t, server.Exists(keys[0]), "old in-flight completions remain excluded after pause expiry")
	newCtx := WithGroupAntiDistillationRequestStart(ctx, time.UnixMilli(lastUntil+1))
	triggered, _, err := svc.observe(newCtx, 7, "new-request", 0, time.Minute)
	require.NoError(t, err)
	require.False(t, triggered, "recovery starts a fresh sample window")
	require.EqualValues(t, 1, svc.redis.LLen(ctx, keys[0]).Val())
}

func TestGroupAntiDistillationConcurrentTriggerIsAtomic(t *testing.T) {
	svc, _ := antiDistillationTestService(t)
	ctx := context.Background()
	var triggeredCount atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			triggered, _, err := svc.observe(ctx, 7, fmt.Sprint(i), 0, 5*time.Minute)
			if err != nil {
				t.Error(err)
			}
			if triggered {
				triggeredCount.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 1, triggeredCount.Load())
}

func TestGroupAntiDistillationInvalidSamplesAndFailuresDoNotPause(t *testing.T) {
	svc, server := antiDistillationTestService(t)
	ctx := context.Background()
	key := &APIKey{UserID: 7, Group: &Group{ID: 1, Platform: PlatformOpenAI}}
	for i := 0; i < 21; i++ {
		svc.Observe(ctx, key, fmt.Sprint(i), 0)
	}
	require.Empty(t, server.Keys(), "disabled groups create no Redis state")
	require.NoError(t, svc.SaveConfig(ctx, 1, GroupAntiDistillationConfig{Enabled: true, PauseMinutes: 5}))
	for i := 0; i < 21; i++ {
		svc.Observe(ctx, key, "", 0)
		svc.Observe(ctx, key, fmt.Sprint(i), -1)
		svc.Observe(ctx, nil, fmt.Sprint(i), 0)
	}
	require.Empty(t, server.Keys(), "unknown/invalid samples have no effect")
	require.NoError(t, svc.redis.Close())
	remaining, err := svc.Check(ctx, 7, 1)
	require.Error(t, err)
	require.Zero(t, remaining, "Redis failure cannot establish a pause")

	svc.settings.(*antiDistillationSettings).err = errors.New("offline")
	svc.mu.Lock()
	svc.configs = nil
	svc.mu.Unlock()
	remaining, err = svc.Check(ctx, 7, 1)
	require.Error(t, err)
	require.Zero(t, remaining, "config failures cannot establish a pause")
}

func TestGroupAntiDistillationConfigCacheBoundedAndRefreshes(t *testing.T) {
	svc, _ := antiDistillationTestService(t)
	ctx := context.Background()
	_, err := svc.GetConfig(ctx, 1)
	require.NoError(t, err)
	settings := svc.settings.(*antiDistillationSettings)
	require.NoError(t, settings.Set(ctx, groupAntiDistillationSettingKey(1), `{"enabled":true,"pause_minutes":12}`))
	svc.mu.Lock()
	cached := svc.configs[1]
	cached.expiresAt = time.Now().Add(-time.Second)
	svc.configs[1] = cached
	svc.mu.Unlock()
	cfg, err := svc.GetConfig(ctx, 1)
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.Equal(t, 12, cfg.PauseMinutes)
	svc.mu.Lock()
	for id := int64(2); id < 1000; id++ {
		svc.cacheConfigLocked(id, DefaultGroupAntiDistillationConfig(), time.Now())
	}
	require.LessOrEqual(t, len(svc.configs), groupAntiDistillationCacheLimit)
	svc.mu.Unlock()
}

type antiDistillationBlockedSettings struct {
	*antiDistillationSettings
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (r *antiDistillationBlockedSettings) GetValue(ctx context.Context, key string) (string, error) {
	value, err := r.antiDistillationSettings.GetValue(ctx, key)
	if key == groupAntiDistillationSettingKey(1) {
		blocked := false
		r.once.Do(func() {
			blocked = true
			close(r.started)
		})
		if blocked {
			select {
			case <-r.release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	return value, err
}

func TestGroupAntiDistillationSlowConfigDoesNotBlockGroupsOrOverwriteSave(t *testing.T) {
	svc, _ := antiDistillationTestService(t)
	ctx := context.Background()
	blocked := &antiDistillationBlockedSettings{
		antiDistillationSettings: svc.settings.(*antiDistillationSettings),
		started:                  make(chan struct{}), release: make(chan struct{}),
	}
	svc.settings = blocked
	finished := make(chan error, 1)
	go func() {
		_, err := svc.GetConfig(ctx, 1)
		finished <- err
	}()
	select {
	case <-blocked.started:
	case <-time.After(time.Second):
		t.Fatal("config read did not start")
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocked.release) }) }
	defer release()
	otherCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err := svc.GetConfig(otherCtx, 2)
	require.NoError(t, err, "one blocked SQL read must not hold the global cache mutex")

	waitCtx, stopWait := context.WithCancel(ctx)
	stopWait()
	_, err = svc.GetConfig(waitCtx, 1)
	require.ErrorIs(t, err, context.Canceled, "a waiter can stop without waiting for the owner SQL")
	require.NoError(t, svc.SaveConfig(otherCtx, 1, GroupAntiDistillationConfig{Enabled: true, PauseMinutes: 9}))
	cfg, err := svc.GetConfig(otherCtx, 1)
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.Equal(t, 9, cfg.PauseMinutes)
	release()
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("pre-save read did not finish")
	}
	cfg, err = svc.GetConfig(otherCtx, 1)
	require.NoError(t, err)
	require.True(t, cfg.Enabled, "pre-save read must not replace the newer cached value")
	require.Equal(t, 9, cfg.PauseMinutes)
}
