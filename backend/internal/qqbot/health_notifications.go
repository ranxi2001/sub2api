package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"slices"
	"time"
)

const healthNotificationEvent = "HEALTH_CHANGE_NOTIFICATION"

type HealthNotificationConfig struct {
	Enabled         bool     `json:"enabled"`
	MentionAll      bool     `json:"mention_all"`
	Groups          []string `json:"groups"`
	MinimumSamples  int      `json:"minimum_samples"`
	ErrorPercent    float64  `json:"error_percent"`
	RecoveryPercent float64  `json:"recovery_percent"`
	TTFTSeconds     int      `json:"ttft_seconds"`
	FailureMinutes  int      `json:"failure_minutes"`
	RecoveryMinutes int      `json:"recovery_minutes"`
	CooldownMinutes int      `json:"cooldown_minutes"`
}

func DefaultHealthNotificationConfig() HealthNotificationConfig {
	return HealthNotificationConfig{Groups: []string{}, MinimumSamples: 50, ErrorPercent: 20, RecoveryPercent: 5, TTFTSeconds: 10, FailureMinutes: 3, RecoveryMinutes: 2, CooldownMinutes: 30}
}

func validateHealthNotifications(c Config) error {
	h := c.HealthNotifications
	if len(h.Groups) > 100 {
		return errors.New("故障通知群最多 100 个")
	}
	for _, g := range h.Groups {
		if !c.allows(g) {
			return errors.New("故障通知群必须在允许使用的群列表中")
		}
	}
	if h.MinimumSamples < 1 || h.MinimumSamples > 1000000 || h.TTFTSeconds < 0 || h.TTFTSeconds > 3600 || h.FailureMinutes < 1 || h.FailureMinutes > 60 || h.RecoveryMinutes < 1 || h.RecoveryMinutes > 60 || h.CooldownMinutes < 0 || h.CooldownMinutes > 1440 {
		return errors.New("故障通知样本量、时长或首 Token 阈值超出范围")
	}
	if math.IsNaN(h.ErrorPercent) || math.IsInf(h.ErrorPercent, 0) || math.IsNaN(h.RecoveryPercent) || math.IsInf(h.RecoveryPercent, 0) || h.ErrorPercent <= 0 || h.ErrorPercent > 100 || h.RecoveryPercent < 0 || h.RecoveryPercent >= h.ErrorPercent {
		return errors.New("错误率阈值须大于 0 且不超过 100%，恢复阈值须低于故障阈值")
	}
	return nil
}

func healthNotificationGroups(c Config) []string {
	c.PriceNotifications.Groups = c.HealthNotifications.Groups
	return priceNotificationGroups(c)
}

type HealthMetric struct {
	Key       string
	Name      string
	Requests  int64
	ErrorRate float64
	TTFTMs    *int64
}

type HealthSnapshot struct {
	UpdatedAt time.Time
	Complete  bool
	Groups    []HealthMetric
}

type HealthSnapshotProvider interface {
	HealthSnapshot(context.Context, time.Time) (HealthSnapshot, error)
}

func (s *Service) SetHealthSnapshotProvider(p HealthSnapshotProvider) { s.healthSource = p }

type healthState struct {
	Active    bool      `json:"active"`
	BadSince  time.Time `json:"bad_since"`
	GoodSince time.Time `json:"good_since"`
	LastSeen  time.Time `json:"last_seen"`
	LastAlert time.Time `json:"last_alert"`
}

// Insufficient, stale, interrupted or incomplete observations never prove a
// failure or recovery. Hysteresis prevents oscillation near the failure limit.
func advanceHealth(state healthState, metric HealthMetric, cfg HealthNotificationConfig, now time.Time, valid bool) (healthState, string) {
	if now.Sub(state.LastSeen) > 90*time.Second || now.Before(state.LastSeen) {
		state.BadSince = time.Time{}
		state.GoodSince = time.Time{}
	}
	state.LastSeen = now
	if !valid || metric.Requests < int64(cfg.MinimumSamples) || math.IsNaN(metric.ErrorRate) || math.IsInf(metric.ErrorRate, 0) || metric.ErrorRate < 0 || metric.ErrorRate > 1 {
		state.BadSince = time.Time{}
		state.GoodSince = time.Time{}
		return state, ""
	}
	bad := metric.ErrorRate*100 >= cfg.ErrorPercent || (cfg.TTFTSeconds > 0 && metric.TTFTMs != nil && *metric.TTFTMs >= int64(cfg.TTFTSeconds)*1000)
	good := metric.ErrorRate*100 <= cfg.RecoveryPercent && (cfg.TTFTSeconds == 0 || (metric.TTFTMs != nil && float64(*metric.TTFTMs) < float64(cfg.TTFTSeconds)*800))
	if !state.Active {
		state.GoodSince = time.Time{}
		if !bad {
			state.BadSince = time.Time{}
			return state, ""
		}
		if state.BadSince.IsZero() {
			state.BadSince = now
		}
		if now.Sub(state.BadSince) >= time.Duration(cfg.FailureMinutes)*time.Minute && now.Sub(state.LastAlert) >= time.Duration(cfg.CooldownMinutes)*time.Minute {
			state.Active = true
			state.LastAlert = now
			state.BadSince = time.Time{}
			return state, "failure"
		}
	} else {
		state.BadSince = time.Time{}
		if !good {
			state.GoodSince = time.Time{}
			return state, ""
		}
		if state.GoodSince.IsZero() {
			state.GoodSince = now
		}
		if now.Sub(state.GoodSince) >= time.Duration(cfg.RecoveryMinutes)*time.Minute {
			state.Active = false
			state.GoodSince = time.Time{}
			return state, "recovery"
		}
	}
	return state, ""
}

func (s *Service) resetHealthNotifications(ctx context.Context, tx *sql.Tx, old, current Config) error {
	if !old.HealthNotifications.Enabled && !current.HealthNotifications.Enabled {
		return nil
	}
	// Mention preferences affect delivery only. Preserve the incident state and
	// queued alerts when the operator changes who a notification mentions.
	oldHealth, currentHealth := old.HealthNotifications, current.HealthNotifications
	oldHealth.MentionAll = currentHealth.MentionAll
	if old.Enabled == current.Enabled && old.AppID == current.AppID && old.Sandbox == current.Sandbox && reflect.DeepEqual(oldHealth, currentHealth) && slices.Equal(healthNotificationGroups(old), healthNotificationGroups(current)) {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM qq_bot_health_snapshots WHERE app_id IN ($1,$2)`, old.AppID, current.AppID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE qq_bot_jobs SET status='cancelled',error='故障通知设置已变更',finished_at=NOW(),payload='{}' WHERE app_id IN ($1,$2) AND event_type=$3 AND status='queued'`, old.AppID, current.AppID, healthNotificationEvent)
	return err
}

func (s *Service) startHealthMonitor(ctx context.Context) {
	if s.healthSource == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				q, cancel := context.WithTimeout(ctx, 20*time.Second)
				err := s.checkHealthChanges(q, time.Now())
				cancel()
				if err != nil && ctx.Err() == nil {
					slog.Warn("qqbot health notification scan failed", "error", err)
				}
			}
		}
	}()
}

func (s *Service) checkHealthChanges(ctx context.Context, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := s.lockConfig(ctx, tx)
	if err != nil {
		return err
	}
	if !c.Enabled || !c.HealthNotifications.Enabled || s.healthSource == nil || len(healthNotificationGroups(c)) == 0 {
		return nil
	}
	var locked bool
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(71427109)`).Scan(&locked); err != nil || !locked {
		return err
	}
	var raw []byte
	var due time.Time
	err = tx.QueryRowContext(ctx, `SELECT snapshot,next_check_at FROM qq_bot_health_snapshots WHERE app_id=$1`, c.AppID).Scan(&raw, &due)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && now.Before(due) {
		return nil
	}
	states := map[string]healthState{}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &states); err != nil {
			return err
		}
	}
	snapshot, err := s.healthSource.HealthSnapshot(ctx, now)
	if err != nil {
		return err
	}
	valid := snapshot.Complete && !snapshot.UpdatedAt.IsZero() && now.Sub(snapshot.UpdatedAt) <= 2*time.Minute && !snapshot.UpdatedAt.After(now.Add(time.Minute))
	present := map[string]bool{}
	batch, seq := randomID(), 0
	continuations := map[string]bool{}
	for _, metric := range snapshot.Groups {
		present[metric.Key] = true
		state, event := advanceHealth(states[metric.Key], metric, c.HealthNotifications, now, valid)
		states[metric.Key] = state
		if event == "" {
			continue
		}
		text := formatHealthNotification(metric, event, now, c.HealthNotifications)
		for _, group := range healthNotificationGroups(c) {
			id := fmt.Sprintf("%s-%08d", batch, seq)
			seq++
			body, _ := json.Marshal(map[string]any{"text": text, "sandbox": c.Sandbox, "continuation": continuations[group]})
			continuations[group] = true
			if _, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,payload,kind) VALUES($1,$2,$1,$3,$4,$5,'health_notification')`, id, c.AppID, healthNotificationEvent, group, string(body)); err != nil {
				return err
			}
		}
	}
	for key, state := range states {
		if !present[key] {
			state.BadSince = time.Time{}
			state.GoodSince = time.Time{}
			states[key] = state
		}
	}
	raw, err = json.Marshal(states)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_health_snapshots(app_id,snapshot,next_check_at) VALUES($1,$2,$3) ON CONFLICT(app_id) DO UPDATE SET snapshot=EXCLUDED.snapshot,next_check_at=EXCLUDED.next_check_at,updated_at=NOW()`, c.AppID, string(raw), now.Add(30*time.Second))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func formatHealthNotification(m HealthMetric, event string, at time.Time, c HealthNotificationConfig) string {
	title, minutes := "⚠️ **渠道异常**", c.FailureMinutes
	if event == "recovery" {
		title = "✅ **渠道恢复**"
		minutes = c.RecoveryMinutes
	}
	text := fmt.Sprintf("%s\n\n分组：%s\n最近 5 分钟成功率：%.2f%%\n状态持续确认：%d 分钟", title, replyPlainLabel(limitText(m.Name, 120)), (1-m.ErrorRate)*100, minutes)
	if m.TTFTMs != nil {
		text += fmt.Sprintf("\n首 Token P50：%.2f 秒", float64(*m.TTFTMs)/1000)
	}
	return text + "\n更新时间：" + at.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("01-02 15:04") + "（北京时间）"
}
