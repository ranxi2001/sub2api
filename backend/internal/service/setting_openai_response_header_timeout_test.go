package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func newOpenAIHeaderTimeoutTestService(t *testing.T, seconds int) *SettingService {
	t.Helper()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIResponseHeaderTimeout = seconds
	return NewSettingService(&panelRateLimitSettingRepo{values: map[string]string{}}, cfg)
}

func TestDefaultOpenAIResponseHeaderTimeoutSettings(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIResponseHeaderTimeout = 20
	settings := DefaultOpenAIResponseHeaderTimeoutSettings(cfg)
	require.True(t, settings.Enabled)
	require.Equal(t, 20, settings.Seconds)
	require.Equal(t, 1, settings.MaxRetries)

	cfg.Gateway.OpenAIResponseHeaderTimeout = 0
	settings = DefaultOpenAIResponseHeaderTimeoutSettings(cfg)
	require.False(t, settings.Enabled)
	require.Equal(t, openAIResponseHeaderTimeoutDefaultSeconds, settings.Seconds)
	require.Equal(t, 1, settings.MaxRetries)

	settings = DefaultOpenAIResponseHeaderTimeoutSettings(nil)
	require.True(t, settings.Enabled)
	require.Equal(t, openAIResponseHeaderTimeoutDefaultSeconds, settings.Seconds)
	require.Equal(t, 1, settings.MaxRetries)
}

func TestOpenAIResponseHeaderTimeoutSettingsGetMissingUsesConfig(t *testing.T) {
	svc := newOpenAIHeaderTimeoutTestService(t, 45)

	settings, err := svc.GetOpenAIResponseHeaderTimeoutSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 45, settings.Seconds)

	seconds, enabled := svc.GetOpenAIResponseHeaderTimeoutCached(context.Background())
	require.True(t, enabled)
	require.Equal(t, 45, seconds)
}

func TestOpenAIResponseHeaderTimeoutSettingsSetAndCache(t *testing.T) {
	svc := newOpenAIHeaderTimeoutTestService(t, 20)

	require.NoError(t, svc.SetOpenAIResponseHeaderTimeoutSettings(context.Background(), &OpenAIResponseHeaderTimeoutSettings{
		Enabled: true, Seconds: 30, MaxRetries: 1,
	}))
	seconds, enabled := svc.GetOpenAIResponseHeaderTimeoutCached(context.Background())
	require.True(t, enabled)
	require.Equal(t, 30, seconds)

	stored, err := svc.GetOpenAIResponseHeaderTimeoutSettings(context.Background())
	require.NoError(t, err)
	require.True(t, stored.Enabled)
	require.Equal(t, 30, stored.Seconds)
	require.Equal(t, 1, stored.MaxRetries)

	require.NoError(t, svc.SetOpenAIResponseHeaderTimeoutSettings(context.Background(), &OpenAIResponseHeaderTimeoutSettings{
		Enabled: false, Seconds: 30, MaxRetries: 1,
	}))
	seconds, enabled = svc.GetOpenAIResponseHeaderTimeoutCached(context.Background())
	require.False(t, enabled)
	require.Equal(t, 30, seconds)
}

func TestOpenAIResponseHeaderRetryLimitUsesAtomicSettingsAndZeroDisables(t *testing.T) {
	for _, retries := range []int{0, 1, 3, 5} {
		t.Run(fmt.Sprint(retries), func(t *testing.T) {
			settings := &panelRateLimitSettingRepo{values: map[string]string{}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(settings, &config.Config{})}
			require.NoError(t, svc.settingService.SetOpenAIResponseHeaderTimeoutSettings(t.Context(),
				&OpenAIResponseHeaderTimeoutSettings{Enabled: true, Seconds: 20, MaxRetries: retries}))
			require.Equal(t, retries, svc.OpenAIResponseHeaderRetryLimit(t.Context()))
			require.NoError(t, svc.settingService.SetOpenAIResponseHeaderTimeoutSettings(t.Context(),
				&OpenAIResponseHeaderTimeoutSettings{Enabled: false, Seconds: 20, MaxRetries: retries}))
			require.Zero(t, svc.OpenAIResponseHeaderRetryLimit(t.Context()))
		})
	}
}

func TestOpenAIResponseHeaderTimeoutSettingsValidation(t *testing.T) {
	svc := newOpenAIHeaderTimeoutTestService(t, 20)
	for _, seconds := range []int{0, -1, openAIResponseHeaderTimeoutMaxSeconds + 1} {
		require.Error(t, svc.SetOpenAIResponseHeaderTimeoutSettings(context.Background(), &OpenAIResponseHeaderTimeoutSettings{
			Enabled: true,
			Seconds: seconds,
		}))
	}
	for _, seconds := range []int{1, 600} {
		require.NoError(t, svc.SetOpenAIResponseHeaderTimeoutSettings(t.Context(), &OpenAIResponseHeaderTimeoutSettings{Enabled: true, Seconds: seconds, MaxRetries: 1}))
	}
	for _, retries := range []int{0, 1, 5} {
		settings := &OpenAIResponseHeaderTimeoutSettings{Enabled: true, Seconds: 20, MaxRetries: retries}
		require.NoError(t, svc.SetOpenAIResponseHeaderTimeoutSettings(t.Context(), settings))
		require.Equal(t, retries, svc.GetOpenAIResponseHeaderTimeoutSettingsCached(t.Context()).MaxRetries)
	}
	for _, retries := range []int{-1, 6} {
		require.Error(t, svc.SetOpenAIResponseHeaderTimeoutSettings(t.Context(), &OpenAIResponseHeaderTimeoutSettings{Enabled: true, Seconds: 20, MaxRetries: retries}))
	}
}

func TestOpenAIResponseHeaderTimeoutLegacySettingInheritsRetryDefault(t *testing.T) {
	repo := &panelRateLimitSettingRepo{values: map[string]string{
		SettingKeyOpenAIResponseHeaderTimeoutSettings: `{"enabled":true,"seconds":37}`,
	}}
	svc := NewSettingService(repo, nil)
	settings, err := svc.GetOpenAIResponseHeaderTimeoutSettings(t.Context())
	require.NoError(t, err)
	require.Equal(t, &OpenAIResponseHeaderTimeoutSettings{Enabled: true, Seconds: 37, MaxRetries: 1}, settings)
}

type headerTimeoutBarrierRepo struct {
	panelRateLimitSettingRepo
	blockNext atomic.Bool
	started   chan struct{}
	release   chan struct{}
	readErr   error
}

func (r *headerTimeoutBarrierRepo) GetValue(ctx context.Context, key string) (string, error) {
	value, err := r.panelRateLimitSettingRepo.GetValue(ctx, key)
	if r.blockNext.Swap(false) {
		close(r.started) // Snapshot taken before the admin save.
		select {
		case <-r.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if r.readErr != nil {
			return "", r.readErr
		}
	}
	return value, err
}

func TestOpenAIResponseHeaderTimeoutStaleRefreshCannotUndoDisable(t *testing.T) {
	for _, readErr := range []error{nil, errors.New("old DB reader failed")} {
		t.Run(map[bool]string{true: "error", false: "success"}[readErr != nil], func(t *testing.T) {
			repo := &headerTimeoutBarrierRepo{
				panelRateLimitSettingRepo: panelRateLimitSettingRepo{values: map[string]string{}},
				started:                   make(chan struct{}), release: make(chan struct{}), readErr: readErr,
			}
			svc := NewSettingService(repo, &config.Config{})
			require.NoError(t, svc.SetOpenAIResponseHeaderTimeoutSettings(t.Context(), &OpenAIResponseHeaderTimeoutSettings{Enabled: true, Seconds: 20, MaxRetries: 1}))
			svc.storeOpenAIResponseHeaderTimeoutCache(OpenAIResponseHeaderTimeoutSettings{Enabled: true, Seconds: 20, MaxRetries: 1}, -time.Second)
			repo.blockNext.Store(true)
			done := make(chan OpenAIResponseHeaderTimeoutSettings, 1)
			go func() {
				done <- svc.GetOpenAIResponseHeaderTimeoutSettingsCached(t.Context())
			}()
			select {
			case <-repo.started:
			case <-time.After(2 * time.Second):
				t.Fatal("old reader did not reach the snapshot barrier")
			}
			save := make(chan error, 1)
			go func() {
				save <- svc.SetOpenAIResponseHeaderTimeoutSettings(t.Context(), &OpenAIResponseHeaderTimeoutSettings{Enabled: false, Seconds: 20, MaxRetries: 0})
			}()
			select {
			case err := <-save:
				require.NoError(t, err)
			case <-time.After(2 * time.Second):
				close(repo.release)
				t.Fatal("save must complete while the old reader is paused")
			}
			saved := svc.openAIResponseHeaderTimeoutCache.Load()
			close(repo.release)
			select {
			case settings := <-done:
				require.False(t, settings.Enabled, "old refresh must return the newer disable")
			case <-time.After(2 * time.Second):
				t.Fatal("old reader did not finish")
			}
			require.Same(t, saved, svc.openAIResponseHeaderTimeoutCache.Load(), "neither success nor error refresh may republish or shorten a newer save's TTL")
			for i := 0; i < 3; i++ {
				seconds, enabled := svc.GetOpenAIResponseHeaderTimeoutCached(t.Context())
				require.False(t, enabled)
				require.Equal(t, 20, seconds)
			}
		})
	}
}

func TestOpenAIResponseHeaderTimeoutStaticZeroAndDBErrors(t *testing.T) {
	cfg := &config.Config{}
	withoutDB := NewSettingService(nil, cfg)
	_, enabled := withoutDB.GetOpenAIResponseHeaderTimeoutCached(t.Context())
	require.False(t, enabled)
	repo := &panelRateLimitSettingRepo{getValueErr: errors.New("DB offline")}
	svc := NewSettingService(repo, cfg)
	_, enabled = svc.GetOpenAIResponseHeaderTimeoutCached(t.Context())
	require.False(t, enabled, "missing DB must keep explicit static 0 disabled")
	svc.storeOpenAIResponseHeaderTimeoutCache(OpenAIResponseHeaderTimeoutSettings{Enabled: false, Seconds: 33, MaxRetries: 1}, -time.Second)
	seconds, enabled := svc.GetOpenAIResponseHeaderTimeoutCached(t.Context())
	require.False(t, enabled)
	require.Equal(t, 33, seconds, "DB errors preserve last known settings")
}
