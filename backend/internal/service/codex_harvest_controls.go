package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const codexHarvestControlsKey = "openai_codex_harvest_controls_v1"

type CodexHarvestSpeed struct {
	RoundIntervalSeconds  int `json:"round_interval_seconds"`
	ProbeIntervalSeconds  int `json:"probe_interval_seconds"`
	AttemptTimeoutSeconds int `json:"attempt_timeout_seconds"`
	CooldownSeconds       int `json:"cooldown_seconds"`
	MaxRequestsPerRound   int `json:"max_requests_per_round"`
	MaxNodeAttempts       int `json:"max_node_attempts"`
	RefreshBeforeSeconds  int `json:"refresh_before_seconds"`
}

type CodexHarvestBound struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type CodexHarvestControls struct {
	EdgeIP                string            `json:"edge_ip"`
	Transport             string            `json:"transport"`
	TargetGateway         string            `json:"target_gateway"`
	Version               int               `json:"version"`
	NodeMemoryEnabled     bool              `json:"node_memory_enabled"`
	TicketFirstScheduling bool              `json:"ticket_first_scheduling"`
	Speed                 CodexHarvestSpeed `json:"speed"`
}

type CodexHarvestRuntime struct {
	Running         bool       `json:"running"`
	NextRoundAt     *time.Time `json:"next_round_at"`
	RequestsUsed    int        `json:"requests_used"`
	RequestBudget   int        `json:"request_budget"`
	CurrentNode     string     `json:"current_node"`
	SelectionReason string     `json:"selection_reason"`
	DegradedReason  string     `json:"degraded_reason"`
}

type CodexHarvestControlSnapshot struct {
	Settings           CodexHarvestControls         `json:"settings"`
	Configured         bool                         `json:"configured"`
	SettingsError      string                       `json:"settings_error"`
	Presets            map[string]CodexHarvestSpeed `json:"presets"`
	Bounds             map[string]CodexHarvestBound `json:"bounds"`
	Defaults           CodexHarvestControls         `json:"defaults"`
	Runtime            CodexHarvestRuntime          `json:"runtime"`
	Available          bool                         `json:"available"`
	AvailabilityReason string                       `json:"availability_reason"`
}

type CodexHarvestService struct {
	nodes       CodexHarvestNodeRepository
	settings    SettingRepository
	defaults    CodexHarvestControls
	configMu    sync.Mutex
	current     CodexHarvestControls
	configured  bool
	configErr   error
	loadedUntil time.Time
	wake        chan struct{}
	runtimeMu   sync.Mutex
	runtime     CodexHarvestRuntime
	lastRequest time.Time
	explore     atomic.Uint64
}

func CodexHarvestSpeedPresets() map[string]CodexHarvestSpeed {
	return map[string]CodexHarvestSpeed{
		"slow": {
			RoundIntervalSeconds: 300, ProbeIntervalSeconds: 5, AttemptTimeoutSeconds: 25,
			CooldownSeconds: 300, MaxRequestsPerRound: 3, MaxNodeAttempts: 2, RefreshBeforeSeconds: 900,
		},
		"standard": {
			RoundIntervalSeconds: 180, ProbeIntervalSeconds: 2, AttemptTimeoutSeconds: 25,
			CooldownSeconds: 180, MaxRequestsPerRound: 6, MaxNodeAttempts: 3, RefreshBeforeSeconds: 600,
		},
		"fast": {
			RoundIntervalSeconds: 60, ProbeIntervalSeconds: 1, AttemptTimeoutSeconds: 25,
			CooldownSeconds: 60, MaxRequestsPerRound: 12, MaxNodeAttempts: 3, RefreshBeforeSeconds: 300,
		},
		"burst": {
			RoundIntervalSeconds: 1, ProbeIntervalSeconds: 0, AttemptTimeoutSeconds: 15,
			CooldownSeconds: 1, MaxRequestsPerRound: 20, MaxNodeAttempts: 5, RefreshBeforeSeconds: 60,
		},
	}
}

var harvestSpeedBounds = []struct {
	name     string
	min, max int
}{
	{"round_interval_seconds", 1, 3600},
	{"probe_interval_seconds", 0, 60},
	{"attempt_timeout_seconds", 1, 120},
	{"cooldown_seconds", 1, 3600},
	{"max_requests_per_round", 1, 100},
	{"max_node_attempts", 1, 10},
	{"refresh_before_seconds", 60, 1800},
}

func CodexHarvestSpeedBounds() map[string]CodexHarvestBound {
	out := make(map[string]CodexHarvestBound, len(harvestSpeedBounds))
	for _, field := range harvestSpeedBounds {
		out[field.name] = CodexHarvestBound{Min: field.min, Max: field.max}
	}
	return out
}

func harvestSpeedValue(speed CodexHarvestSpeed, name string) int {
	switch name {
	case "round_interval_seconds":
		return speed.RoundIntervalSeconds
	case "probe_interval_seconds":
		return speed.ProbeIntervalSeconds
	case "attempt_timeout_seconds":
		return speed.AttemptTimeoutSeconds
	case "cooldown_seconds":
		return speed.CooldownSeconds
	case "max_requests_per_round":
		return speed.MaxRequestsPerRound
	case "max_node_attempts":
		return speed.MaxNodeAttempts
	case "refresh_before_seconds":
		return speed.RefreshBeforeSeconds
	default:
		return 0
	}
}

func applyHarvestSpeed(cfg *config.OpenAICodexTicketConfig, speed CodexHarvestSpeed) {
	if cfg == nil {
		return
	}
	if speed.RoundIntervalSeconds > 0 {
		cfg.HarvestProbeIntervalSeconds = speed.RoundIntervalSeconds
	}
	if speed.CooldownSeconds > 0 {
		cfg.HarvestCooldownSeconds = speed.CooldownSeconds
	}
	if speed.AttemptTimeoutSeconds > 0 {
		cfg.HarvestAttemptTimeoutSeconds = speed.AttemptTimeoutSeconds
	}
	if speed.MaxRequestsPerRound > 0 {
		cfg.MaxProbesPerRound = speed.MaxRequestsPerRound
	}
	if speed.RefreshBeforeSeconds > 0 {
		cfg.RefreshBeforeSeconds = speed.RefreshBeforeSeconds
	}
}

func normalizeCodexHarvestControls(v *CodexHarvestControls) {
	if v == nil {
		return
	}
	v.TargetGateway = normalizeCodex780Gateway(v.TargetGateway)
	if v.Transport == "" {
		v.Transport = "sse"
	}
	if v.TargetGateway == "" {
		v.TargetGateway = "unified-88"
	}
	if v.Speed.RefreshBeforeSeconds <= 0 {
		v.Speed.RefreshBeforeSeconds = 600
	}
}

func NewCodexHarvestService(nodes CodexHarvestNodeRepository, settings SettingRepository, cfg *config.Config) *CodexHarvestService {
	v := CodexHarvestControls{Version: 1, Speed: CodexHarvestSpeedPresets()["standard"]}
	if cfg != nil {
		base := cfg.Gateway.OpenAICodexTicket
		if base.HarvestProbeIntervalSeconds >= 30 {
			v.Speed.RoundIntervalSeconds = base.HarvestProbeIntervalSeconds
		}
		if base.HarvestCooldownSeconds > 0 {
			v.Speed.CooldownSeconds = base.HarvestCooldownSeconds
		}
		if base.HarvestAttemptTimeoutSeconds > 0 {
			v.Speed.AttemptTimeoutSeconds = base.HarvestAttemptTimeoutSeconds
		}
		if base.MaxProbesPerRound > 0 {
			v.Speed.MaxRequestsPerRound = base.MaxProbesPerRound
		}
		if base.RefreshBeforeSeconds > 0 {
			v.Speed.RefreshBeforeSeconds = base.RefreshBeforeSeconds
		}
	}
	normalizeCodexHarvestControls(&v)
	return &CodexHarvestService{nodes: nodes, settings: settings, defaults: v, current: v, wake: make(chan struct{}, 1)}
}

func ProvideCodexHarvestService(nodes CodexHarvestNodeRepository, settings SettingRepository, cfg *config.Config, flows CodexHarvestFlowRepository) *CodexHarvestService {
	bindCodexHarvestFlowStore(flows)
	return NewCodexHarvestService(nodes, settings, cfg)
}

func ValidateCodexHarvestControls(v CodexHarvestControls) error {
	normalizeCodexHarvestControls(&v)
	if err := validateCodexMintEdgeIP(v.EdgeIP); err != nil {
		return err
	}
	if v.Transport != "sse" && v.Transport != "websocket" {
		return errors.New("transport must be sse or websocket")
	}
	if v.TargetGateway != "any" && !regexp.MustCompile(`^unified-[0-9]{1,5}$`).MatchString(v.TargetGateway) {
		return errors.New("target_gateway must be any, unified-N or chat.gateway.unified-N.api.openai.com")
	}
	if v.Version != 1 {
		return errors.New("unsupported harvest settings version")
	}
	for _, field := range harvestSpeedBounds {
		value := harvestSpeedValue(v.Speed, field.name)
		if value < field.min || value > field.max {
			return fmt.Errorf("%s must be between %d and %d", field.name, field.min, field.max)
		}
	}
	return nil
}

func (s *CodexHarvestService) Controls(ctx context.Context) (CodexHarvestControls, bool, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if time.Now().Before(s.loadedUntil) {
		return s.current, s.configured, s.configErr
	}
	query, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := s.settings.GetValue(query, codexHarvestControlsKey)
	if errors.Is(err, ErrSettingNotFound) {
		err = nil
		raw = ""
	}
	v := s.defaults
	if err == nil && raw != "" {
		err = json.Unmarshal([]byte(raw), &v)
		if err == nil {
			normalizeCodexHarvestControls(&v)
			err = ValidateCodexHarvestControls(v)
		}
	}
	if err == nil {
		s.current = v
		s.configured = raw != ""
	}
	s.configErr = err
	s.loadedUntil = time.Now().Add(3 * time.Second)
	return s.current, s.configured, err
}

func (s *CodexHarvestService) SaveControls(ctx context.Context, v CodexHarvestControls) error {
	normalizeCodexHarvestControls(&v)
	if err := ValidateCodexHarvestControls(v); err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.configMu.Lock()
	query, cancel := context.WithTimeout(ctx, 3*time.Second)
	err = s.settings.Set(query, codexHarvestControlsKey, string(raw))
	cancel()
	if err == nil {
		s.current = v
		s.configured = true
		s.configErr = nil
		s.loadedUntil = time.Now().Add(3 * time.Second)
	}
	s.configMu.Unlock()
	if err != nil {
		return err
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *CodexHarvestService) setRuntime(update func(*CodexHarvestRuntime)) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	update(&s.runtime)
}

func (s *CodexHarvestService) Runtime() CodexHarvestRuntime {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	return s.runtime
}

func (s *CodexHarvestService) degrade(reason string) {
	s.setRuntime(func(r *CodexHarvestRuntime) { r.DegradedReason = reason })
}

func (s *CodexHarvestService) ListNodes(ctx context.Context, offset, limit int) (CodexHarvestNodePage, error) {
	query, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.nodes.List(query, offset, limit)
}

func (s *CodexHarvestService) ResetNodes(ctx context.Context, id int64) error {
	query, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.nodes.Reset(query, id); err != nil {
		return err
	}
	s.explore.Store(0)
	return nil
}
