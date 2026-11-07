package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	openAIResponseHeaderTimeoutMinSeconds     = 1
	openAIResponseHeaderTimeoutMaxSeconds     = 600
	openAIResponseHeaderTimeoutDefaultSeconds = 20
	openAIImageResponseHeaderTimeoutSeconds   = 180
	openAIResponseHeaderTimeoutCacheTTL       = 60 * time.Second
	openAIResponseHeaderTimeoutErrorTTL       = 5 * time.Second
	openAIResponseHeaderTimeoutDBTimeout      = 5 * time.Second
)

// OpenAIResponseHeaderTimeoutSettings controls how long an OpenAI/Codex HTTP
// upstream may take to send response headers before a terminal timeout.
// The setting is DB-backed so it can be changed from the admin UI; the zero
// value of Seconds means "use the default".
type OpenAIResponseHeaderTimeoutSettings struct {
	Enabled bool `json:"enabled"`
	Seconds int  `json:"seconds"`
	// ImageGroupIDs lists the groups whose image requests wait ImageSeconds
	// instead of Seconds. Image upstreams may send no header until the image is
	// finished, so an empty list keeps the single global timeout.
	ImageGroupIDs []int64 `json:"image_group_ids"`
	ImageSeconds  int     `json:"image_seconds"`
}

type cachedOpenAIResponseHeaderTimeoutSettings struct {
	settings  OpenAIResponseHeaderTimeoutSettings
	expiresAt int64
}

// DefaultOpenAIResponseHeaderTimeoutSettings uses the static config as the
// default, so existing deployments keep their configured behavior until an
// admin saves a DB override.
func DefaultOpenAIResponseHeaderTimeoutSettings(cfg *config.Config) *OpenAIResponseHeaderTimeoutSettings {
	seconds := openAIResponseHeaderTimeoutDefaultSeconds
	enabled := false
	if cfg != nil && cfg.Gateway.OpenAIResponseHeaderTimeout > 0 {
		seconds = cfg.Gateway.OpenAIResponseHeaderTimeout
		enabled = true
	}
	return &OpenAIResponseHeaderTimeoutSettings{
		Enabled:       enabled,
		Seconds:       seconds,
		ImageGroupIDs: []int64{},
		ImageSeconds:  openAIImageResponseHeaderTimeoutSeconds,
	}
}

func normalizeOpenAIResponseHeaderTimeoutSettings(settings, fallback *OpenAIResponseHeaderTimeoutSettings) {
	if settings == nil || fallback == nil {
		return
	}
	if settings.Seconds < openAIResponseHeaderTimeoutMinSeconds || settings.Seconds > openAIResponseHeaderTimeoutMaxSeconds {
		settings.Seconds = fallback.Seconds
	}
	if settings.ImageSeconds < openAIResponseHeaderTimeoutMinSeconds || settings.ImageSeconds > openAIResponseHeaderTimeoutMaxSeconds {
		settings.ImageSeconds = fallback.ImageSeconds
	}
	settings.ImageGroupIDs = normalizeOpenAIImageTimeoutGroupIDs(settings.ImageGroupIDs)
}

func normalizeOpenAIImageTimeoutGroupIDs(groupIDs []int64) []int64 {
	normalized := make([]int64, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID > 0 {
			normalized = append(normalized, groupID)
		}
	}
	slices.Sort(normalized)
	return slices.Compact(normalized)
}

// GetOpenAIResponseHeaderTimeoutSettings reads the DB override (admin handler
// path). Missing/invalid values fall back to the static config.
func (s *SettingService) GetOpenAIResponseHeaderTimeoutSettings(ctx context.Context) (*OpenAIResponseHeaderTimeoutSettings, error) {
	fallback := DefaultOpenAIResponseHeaderTimeoutSettings(s.cfg)
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIResponseHeaderTimeoutSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return fallback, nil
		}
		return nil, fmt.Errorf("get OpenAI response header timeout settings: %w", err)
	}
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}

	settings := &OpenAIResponseHeaderTimeoutSettings{}
	if err := json.Unmarshal([]byte(value), settings); err != nil {
		slog.Warn("failed to unmarshal OpenAI response header timeout settings, falling back to defaults",
			"error", err, "key", SettingKeyOpenAIResponseHeaderTimeoutSettings)
		return fallback, nil
	}
	normalizeOpenAIResponseHeaderTimeoutSettings(settings, fallback)
	return settings, nil
}

// SetOpenAIResponseHeaderTimeoutSettings validates and persists the admin
// override, then refreshes the process-local cache so this node applies it
// immediately. Other nodes refresh within the 60s cache TTL.
func (s *SettingService) SetOpenAIResponseHeaderTimeoutSettings(ctx context.Context, settings *OpenAIResponseHeaderTimeoutSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}
	if settings.Seconds < openAIResponseHeaderTimeoutMinSeconds || settings.Seconds > openAIResponseHeaderTimeoutMaxSeconds {
		return fmt.Errorf("seconds must be between %d-%d", openAIResponseHeaderTimeoutMinSeconds, openAIResponseHeaderTimeoutMaxSeconds)
	}
	saved := *settings
	if saved.ImageSeconds == 0 {
		saved.ImageSeconds = openAIImageResponseHeaderTimeoutSeconds
	}
	if saved.ImageSeconds < openAIResponseHeaderTimeoutMinSeconds || saved.ImageSeconds > openAIResponseHeaderTimeoutMaxSeconds {
		return fmt.Errorf("image_seconds must be between %d-%d", openAIResponseHeaderTimeoutMinSeconds, openAIResponseHeaderTimeoutMaxSeconds)
	}
	saved.ImageGroupIDs = normalizeOpenAIImageTimeoutGroupIDs(saved.ImageGroupIDs)

	data, err := json.Marshal(saved)
	if err != nil {
		return fmt.Errorf("marshal OpenAI response header timeout settings: %w", err)
	}
	// Serialize saves and cache publication, but never hold this lock during a
	// refresh read: an old DB snapshot must not block a newer admin save.
	s.openAIResponseHeaderTimeoutMu.Lock()
	defer s.openAIResponseHeaderTimeoutMu.Unlock()
	if err := s.settingRepo.Set(ctx, SettingKeyOpenAIResponseHeaderTimeoutSettings, string(data)); err != nil {
		return err
	}
	s.openAIResponseHeaderTimeoutGen++
	s.storeOpenAIResponseHeaderTimeoutCache(saved, openAIResponseHeaderTimeoutCacheTTL)
	return nil
}

// GetOpenAIResponseHeaderTimeoutCached returns (seconds, enabled) for the
// transport hot path. It never blocks on a DB read when the cache is fresh and
// preserves the last known value on DB errors.
func (s *SettingService) GetOpenAIResponseHeaderTimeoutCached(ctx context.Context) (int, bool) {
	settings := s.cachedOpenAIResponseHeaderTimeoutSettings(ctx)
	return settings.Seconds, settings.Enabled
}

// OpenAIImageResponseHeaderTimeout returns the longer wait for image requests
// in a selected group, or zero when the global timeout applies.
func (s *SettingService) OpenAIImageResponseHeaderTimeout(ctx context.Context, groupID *int64) time.Duration {
	if groupID == nil {
		return 0
	}
	settings := s.cachedOpenAIResponseHeaderTimeoutSettings(ctx)
	if !settings.Enabled || settings.ImageSeconds <= settings.Seconds || !slices.Contains(settings.ImageGroupIDs, *groupID) {
		return 0
	}
	return time.Duration(settings.ImageSeconds) * time.Second
}

func (s *SettingService) cachedOpenAIResponseHeaderTimeoutSettings(ctx context.Context) OpenAIResponseHeaderTimeoutSettings {
	if s == nil {
		return *DefaultOpenAIResponseHeaderTimeoutSettings(nil)
	}
	if s.settingRepo == nil {
		return *DefaultOpenAIResponseHeaderTimeoutSettings(s.cfg)
	}
	if cached, ok := s.openAIResponseHeaderTimeoutCache.Load().(*cachedOpenAIResponseHeaderTimeoutSettings); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.settings
		}
	}

	result, _, _ := s.openAIResponseHeaderTimeoutSF.Do("openai_response_header_timeout_settings", func() (any, error) {
		s.openAIResponseHeaderTimeoutMu.Lock()
		generation := s.openAIResponseHeaderTimeoutGen
		if cached, ok := s.openAIResponseHeaderTimeoutCache.Load().(*cachedOpenAIResponseHeaderTimeoutSettings); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				s.openAIResponseHeaderTimeoutMu.Unlock()
				return cached.settings, nil
			}
		}
		s.openAIResponseHeaderTimeoutMu.Unlock()
		if ctx == nil {
			ctx = context.Background()
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIResponseHeaderTimeoutDBTimeout)
		defer cancel()

		settings, err := s.GetOpenAIResponseHeaderTimeoutSettings(dbCtx)
		s.openAIResponseHeaderTimeoutMu.Lock()
		defer s.openAIResponseHeaderTimeoutMu.Unlock()
		if generation != s.openAIResponseHeaderTimeoutGen {
			if current, ok := s.openAIResponseHeaderTimeoutCache.Load().(*cachedOpenAIResponseHeaderTimeoutSettings); ok && current != nil {
				return current.settings, nil
			}
		}
		if err != nil {
			slog.Warn("failed to get OpenAI response header timeout settings", "error", err)
			fallback := *DefaultOpenAIResponseHeaderTimeoutSettings(s.cfg)
			if prior, ok := s.openAIResponseHeaderTimeoutCache.Load().(*cachedOpenAIResponseHeaderTimeoutSettings); ok && prior != nil {
				fallback = prior.settings
			}
			s.storeOpenAIResponseHeaderTimeoutCache(fallback, openAIResponseHeaderTimeoutErrorTTL)
			return fallback, nil
		}
		s.storeOpenAIResponseHeaderTimeoutCache(*settings, openAIResponseHeaderTimeoutCacheTTL)
		return *settings, nil
	})
	if settings, ok := result.(OpenAIResponseHeaderTimeoutSettings); ok {
		return settings
	}
	return *DefaultOpenAIResponseHeaderTimeoutSettings(s.cfg)
}

func (s *SettingService) storeOpenAIResponseHeaderTimeoutCache(settings OpenAIResponseHeaderTimeoutSettings, ttl time.Duration) {
	s.openAIResponseHeaderTimeoutCache.Store(&cachedOpenAIResponseHeaderTimeoutSettings{
		settings:  settings,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}
