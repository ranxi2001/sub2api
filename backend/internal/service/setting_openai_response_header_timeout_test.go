package service

import (
	"context"
	"errors"
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

	cfg.Gateway.OpenAIResponseHeaderTimeout = 0
	settings = DefaultOpenAIResponseHeaderTimeoutSettings(cfg)
	require.False(t, settings.Enabled)
	require.Equal(t, openAIResponseHeaderTimeoutDefaultSeconds, settings.Seconds)

	settings = DefaultOpenAIResponseHeaderTimeoutSettings(nil)
	require.False(t, settings.Enabled)
	require.Equal(t, openAIResponseHeaderTimeoutDefaultSeconds, settings.Seconds)
}

func TestOpenAIResponseHeaderTimeoutNilFallbackDisabled(t *testing.T) {
	for name, svc := range map[string]*SettingService{
		"nil service": nil,
		"nil config":  NewSettingService(nil, nil),
	} {
		t.Run(name, func(t *testing.T) {
			seconds, enabled := svc.GetOpenAIResponseHeaderTimeoutCached(t.Context())
			require.False(t, enabled)
			require.Equal(t, 20, seconds, "retain valid seconds for manual enabling")
		})
	}
}

func TestOpenAIResponseHeaderTimeoutSettingsGetMissingDefaultDisabled(t *testing.T) {
	for name, cfg := range map[string]*config.Config{
		"nil config": nil,
		"default":    {},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &panelRateLimitSettingRepo{values: map[string]string{}}
			svc := NewSettingService(repo, cfg)
			settings, err := svc.GetOpenAIResponseHeaderTimeoutSettings(t.Context())
			require.NoError(t, err)
			require.Equal(t, &OpenAIResponseHeaderTimeoutSettings{Enabled: false, Seconds: 20, ImageGroupIDs: []int64{}, ImageSeconds: 180}, settings)

			seconds, enabled := svc.GetOpenAIResponseHeaderTimeoutCached(t.Context())
			require.False(t, enabled)
			require.Equal(t, 20, seconds)
			require.Empty(t, repo.values, "reading defaults must not persist an override")
		})
	}
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

func TestOpenAIResponseHeaderTimeoutPersistedSettingsOverrideDefaults(t *testing.T) {
	optIn := &config.Config{}
	optIn.Gateway.OpenAIResponseHeaderTimeout = 45
	for name, cfg := range map[string]*config.Config{
		"nil config": nil,
		"default":    {},
		"opt in":     optIn,
	} {
		for _, saved := range []struct {
			name  string
			value string
			want  OpenAIResponseHeaderTimeoutSettings
		}{
			{"enabled", `{"enabled":true,"seconds":30}`, OpenAIResponseHeaderTimeoutSettings{Enabled: true, Seconds: 30, ImageGroupIDs: []int64{}, ImageSeconds: 180}},
			{"disabled", `{"enabled":false,"seconds":33}`, OpenAIResponseHeaderTimeoutSettings{Enabled: false, Seconds: 33, ImageGroupIDs: []int64{}, ImageSeconds: 180}},
		} {
			t.Run(name+"/"+saved.name, func(t *testing.T) {
				repo := &panelRateLimitSettingRepo{values: map[string]string{SettingKeyOpenAIResponseHeaderTimeoutSettings: saved.value}}
				svc := NewSettingService(repo, cfg)
				settings, err := svc.GetOpenAIResponseHeaderTimeoutSettings(t.Context())
				require.NoError(t, err)
				require.Equal(t, &saved.want, settings)

				seconds, enabled := svc.GetOpenAIResponseHeaderTimeoutCached(t.Context())
				require.Equal(t, saved.want.Enabled, enabled)
				require.Equal(t, saved.want.Seconds, seconds)
				require.Equal(t, saved.value, repo.values[SettingKeyOpenAIResponseHeaderTimeoutSettings])
			})
		}
	}
}

func TestOpenAIResponseHeaderTimeoutSettingsSetAndCache(t *testing.T) {
	svc := newOpenAIHeaderTimeoutTestService(t, 20)

	require.NoError(t, svc.SetOpenAIResponseHeaderTimeoutSettings(context.Background(), &OpenAIResponseHeaderTimeoutSettings{
		Enabled: true,
		Seconds: 30,
	}))
	seconds, enabled := svc.GetOpenAIResponseHeaderTimeoutCached(context.Background())
	require.True(t, enabled)
	require.Equal(t, 30, seconds)

	stored, err := svc.GetOpenAIResponseHeaderTimeoutSettings(context.Background())
	require.NoError(t, err)
	require.True(t, stored.Enabled)
	require.Equal(t, 30, stored.Seconds)

	require.NoError(t, svc.SetOpenAIResponseHeaderTimeoutSettings(context.Background(), &OpenAIResponseHeaderTimeoutSettings{
		Enabled: false,
		Seconds: 30,
	}))
	seconds, enabled = svc.GetOpenAIResponseHeaderTimeoutCached(context.Background())
	require.False(t, enabled)
	require.Equal(t, 30, seconds)

	stored, err = svc.GetOpenAIResponseHeaderTimeoutSettings(context.Background())
	require.NoError(t, err)
	require.False(t, stored.Enabled)
	require.Equal(t, 30, stored.Seconds)
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
		require.NoError(t, svc.SetOpenAIResponseHeaderTimeoutSettings(t.Context(), &OpenAIResponseHeaderTimeoutSettings{Enabled: true, Seconds: seconds}))
	}
	require.Error(t, svc.SetOpenAIResponseHeaderTimeoutSettings(t.Context(), &OpenAIResponseHeaderTimeoutSettings{Enabled: false, Seconds: 0}))
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
			require.NoError(t, svc.SetOpenAIResponseHeaderTimeoutSettings(t.Context(), &OpenAIResponseHeaderTimeoutSettings{Enabled: true, Seconds: 20}))
			svc.storeOpenAIResponseHeaderTimeoutCache(OpenAIResponseHeaderTimeoutSettings{Enabled: true, Seconds: 20}, -time.Second)
			repo.blockNext.Store(true)
			done := make(chan OpenAIResponseHeaderTimeoutSettings, 1)
			go func() {
				seconds, enabled := svc.GetOpenAIResponseHeaderTimeoutCached(t.Context())
				done <- OpenAIResponseHeaderTimeoutSettings{Enabled: enabled, Seconds: seconds}
			}()
			select {
			case <-repo.started:
			case <-time.After(2 * time.Second):
				t.Fatal("old reader did not reach the snapshot barrier")
			}
			save := make(chan error, 1)
			go func() {
				save <- svc.SetOpenAIResponseHeaderTimeoutSettings(t.Context(), &OpenAIResponseHeaderTimeoutSettings{Enabled: false, Seconds: 20})
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
	svc.storeOpenAIResponseHeaderTimeoutCache(OpenAIResponseHeaderTimeoutSettings{Enabled: false, Seconds: 33}, -time.Second)
	seconds, enabled := svc.GetOpenAIResponseHeaderTimeoutCached(t.Context())
	require.False(t, enabled)
	require.Equal(t, 33, seconds, "DB errors preserve last known settings")
}
