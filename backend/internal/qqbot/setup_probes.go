package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const setupProbeKey = "qq_bot_setup_probes"

type SetupProbe struct {
	Kind    string    `json:"kind"`
	Status  string    `json:"status"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}
type setupProbeState struct {
	Fingerprint string                `json:"fingerprint"`
	Results     map[string]SetupProbe `json:"results"`
}

func probeFingerprint(c Config) string {
	return ConfigVersion([]any{c.AppID, c.AppSecret, c.Sandbox, c.BaseURL, c.APIKey, c.ChatModel, c.ChatProtocol, c.MaxOutputTokens, c.ChatDisabled, c.ImageEnabled, c.ImageAIConnection, c.ImageBaseURL, c.ImageAPIKey, c.ImageModel, c.ImageSize, c.ImageOutputFormat})
}
func requiredSetupProbes(c Config) []string {
	out := []string{"qq"}
	if !c.ChatDisabled {
		out = append(out, "chat_flow")
	}
	if c.ImageEnabled {
		out = append(out, "image", "image_edit")
		if !c.ChatDisabled {
			out = append(out, "vision")
		}
	}
	return out
}
func (s *Service) SetupProbe(ctx context.Context, kind string) (SetupProbe, error) {
	allowed := map[string]bool{"qq": true, "chat_flow": true, "chat": true, "vision": true, "tools": true, "image": true, "image_edit": true}
	if !allowed[kind] {
		return SetupProbe{}, errors.New("未知能力测试")
	}
	// One explicit probe at a time across replicas. Do not automatically retry paid calls.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SetupProbe{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var acquired bool
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(71427107)`).Scan(&acquired); err != nil {
		return SetupProbe{}, err
	}
	if !acquired {
		return SetupProbe{}, errors.New("已有能力测试正在执行，请等待完成后再试")
	}
	c, err := s.loadConfig(ctx, tx)
	if err != nil {
		return SetupProbe{}, err
	}
	fingerprint := probeFingerprint(c)
	result := SetupProbe{Kind: kind, Status: "ready", At: time.Now()}
	result.Message, err = s.probeConfig(ctx, c, kind)
	if err != nil {
		result.Status = "blocked"
		result.Message = err.Error()
	}
	for _, secret := range []string{c.AppSecret, c.APIKey, c.ImageAPIKey} {
		if secret != "" {
			result.Message = strings.ReplaceAll(result.Message, secret, "[redacted]")
		}
	}
	result.Message = limitText(result.Message, 1000)
	// The model request runs without holding the config lock. Recheck before persisting.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(71427101)`); err != nil {
		return result, err
	}
	current, e := s.loadConfig(ctx, tx)
	if e != nil {
		return result, e
	}
	if probeFingerprint(current) != fingerprint {
		return result, ErrConfigConflict
	}
	stored := setupProbeState{}
	if _, e = readActivityConfig(ctx, tx, setupProbeKey, &stored); e != nil {
		return result, e
	}
	if stored.Fingerprint != fingerprint || stored.Results == nil {
		stored = setupProbeState{Fingerprint: fingerprint, Results: map[string]SetupProbe{}}
	}
	stored.Results[kind] = result
	raw, e := json.Marshal(stored)
	if e != nil {
		return result, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,$2,NOW()) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=NOW()`, setupProbeKey, string(raw)); e != nil {
		return result, e
	}
	return result, tx.Commit()
}
func readSetupProbes(ctx context.Context, db queryRower, c Config) (map[string]SetupProbe, error) {
	var stored setupProbeState
	_, err := readActivityConfig(ctx, db, setupProbeKey, &stored)
	if err != nil {
		return nil, err
	}
	out := map[string]SetupProbe{}
	if stored.Fingerprint == probeFingerprint(c) {
		for kind, r := range stored.Results {
			if time.Since(r.At) < 24*time.Hour && time.Since(r.At) >= 0 {
				out[kind] = r
			}
		}
	}
	return out, nil
}
func validateSetupActivation(ctx context.Context, tx *sql.Tx, c Config) error {
	probes, err := readSetupProbes(ctx, tx, c)
	if err != nil {
		return err
	}
	for _, kind := range requiredSetupProbes(c) {
		if probes[kind].Status != "ready" {
			return errors.New("请先保存配置并完成所选能力的连接测试，再启用机器人")
		}
	}
	return nil
}
