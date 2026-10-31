package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	openAIResponseHeaderTimeoutMinSeconds     = 1
	openAIResponseHeaderTimeoutMaxSeconds     = 600
	openAIResponseHeaderTimeoutDefaultSeconds = 20
	openAIResponseHeaderTimeoutDefaultRetries = 1
	openAIResponseHeaderTimeoutMaxRetries     = 5
	openAIResponseHeaderTimeoutCacheTTL       = 60 * time.Second
	openAIResponseHeaderTimeoutErrorTTL       = 5 * time.Second
	openAIResponseHeaderTimeoutDBTimeout      = 5 * time.Second
)

// OpenAIResponseHeaderTimeoutSettings controls how long an OpenAI/Codex HTTP
// upstream may take to send response headers before a terminal timeout.
// The setting is DB-backed so it can be changed from the admin UI; the zero
// value of Seconds means "use the default".
type OpenAIResponseHeaderTimeoutSettings struct {
	Enabled    bool `json:"enabled"`
	Seconds    int  `json:"seconds"`
	MaxRetries int  `json:"max_retries"`
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
	enabled := true
	if cfg != nil {
		configured := cfg.Gateway.OpenAIResponseHeaderTimeout
		if configured > 0 {
			seconds = configured
		} else {
			enabled = false
		}
	}
	return &OpenAIResponseHeaderTimeoutSettings{Enabled: enabled, Seconds: seconds, MaxRetries: openAIResponseHeaderTimeoutDefaultRetries}
}

func normalizeOpenAIResponseHeaderTimeoutSettings(settings, fallback *OpenAIResponseHeaderTimeoutSettings) {
	if settings == nil || fallback == nil {
		return
	}
	if settings.Seconds < openAIResponseHeaderTimeoutMinSeconds || settings.Seconds > openAIResponseHeaderTimeoutMaxSeconds {
		settings.Seconds = fallback.Seconds
	}
	if settings.MaxRetries < 0 || settings.MaxRetries > openAIResponseHeaderTimeoutMaxRetries {
		settings.MaxRetries = fallback.MaxRetries
	}
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

	// Decode additive fields as pointers so a pre-max_retries database value
	// inherits the default retry budget instead of silently disabling retries.
	stored := struct {
		Enabled    *bool `json:"enabled"`
		Seconds    *int  `json:"seconds"`
		MaxRetries *int  `json:"max_retries"`
	}{}
	if err := json.Unmarshal([]byte(value), &stored); err != nil {
		slog.Warn("failed to unmarshal OpenAI response header timeout settings, falling back to defaults",
			"error", err, "key", SettingKeyOpenAIResponseHeaderTimeoutSettings)
		return fallback, nil
	}
	settings := &OpenAIResponseHeaderTimeoutSettings{
		Enabled: fallback.Enabled, Seconds: fallback.Seconds, MaxRetries: fallback.MaxRetries,
	}
	if stored.Enabled != nil {
		settings.Enabled = *stored.Enabled
	}
	if stored.Seconds != nil {
		settings.Seconds = *stored.Seconds
	}
	if stored.MaxRetries != nil {
		settings.MaxRetries = *stored.MaxRetries
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
	if settings.MaxRetries < 0 || settings.MaxRetries > openAIResponseHeaderTimeoutMaxRetries {
		return fmt.Errorf("max_retries must be between 0-%d", openAIResponseHeaderTimeoutMaxRetries)
	}

	data, err := json.Marshal(settings)
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
	s.storeOpenAIResponseHeaderTimeoutCache(*settings, openAIResponseHeaderTimeoutCacheTTL)
	return nil
}

// GetOpenAIResponseHeaderTimeoutCached returns (seconds, enabled) for the
// transport hot path. It never blocks on a DB read when the cache is fresh and
// preserves the last known value on DB errors.
func (s *SettingService) GetOpenAIResponseHeaderTimeoutCached(ctx context.Context) (int, bool) {
	settings := s.getOpenAIResponseHeaderTimeoutCached(ctx)
	return settings.Seconds, settings.Enabled
}

func (s *SettingService) GetOpenAIResponseHeaderTimeoutSettingsCached(ctx context.Context) OpenAIResponseHeaderTimeoutSettings {
	return s.getOpenAIResponseHeaderTimeoutCached(ctx)
}

// OpenAIResponseHeaderRetryLimit returns additional attempts after the first
// submission. Zero disables replay. Read from the same atomically published
// settings snapshot as the response-header timeout.
func (s *OpenAIGatewayService) OpenAIResponseHeaderRetryLimit(ctx context.Context) int {
	var settings OpenAIResponseHeaderTimeoutSettings
	if s != nil && s.settingService != nil {
		settings = s.settingService.GetOpenAIResponseHeaderTimeoutSettingsCached(ctx)
	} else {
		var cfg *config.Config
		if s != nil {
			cfg = s.cfg
		}
		settings = *DefaultOpenAIResponseHeaderTimeoutSettings(cfg)
	}
	if !settings.Enabled {
		return 0
	}
	return settings.MaxRetries
}

func (s *SettingService) getOpenAIResponseHeaderTimeoutCached(ctx context.Context) OpenAIResponseHeaderTimeoutSettings {
	if s == nil {
		fallback := DefaultOpenAIResponseHeaderTimeoutSettings(nil)
		return *fallback
	}
	if s.settingRepo == nil {
		fallback := DefaultOpenAIResponseHeaderTimeoutSettings(s.cfg)
		return *fallback
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
	fallback := DefaultOpenAIResponseHeaderTimeoutSettings(s.cfg)
	return *fallback
}

func (s *SettingService) storeOpenAIResponseHeaderTimeoutCache(settings OpenAIResponseHeaderTimeoutSettings, ttl time.Duration) {
	s.openAIResponseHeaderTimeoutCache.Store(&cachedOpenAIResponseHeaderTimeoutSettings{
		settings:  settings,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}
