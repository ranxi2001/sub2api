package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

const priceNotificationEvent = "PRICE_CHANGE_NOTIFICATION"

// Disabled by default. Empty Groups means all AllowedGroups.
type PriceNotificationConfig struct {
	Enabled    bool     `json:"enabled"`
	Scheduled  bool     `json:"scheduled"`
	MentionAll bool     `json:"mention_all"`
	Groups     []string `json:"groups"`
}

type PriceField struct {
	Key       string `json:"key"`
	Model     string `json:"model,omitempty"`
	Label     string `json:"label"`
	Value     string `json:"value"`
	Unit      string `json:"unit,omitempty"`
	Scheduled bool   `json:"scheduled,omitempty"`
}

type PriceState struct {
	Key    string       `json:"key"`
	Kind   string       `json:"kind"`
	Name   string       `json:"name"`
	Status string       `json:"status,omitempty"`
	Fields []PriceField `json:"fields"`
}

type PriceSnapshot struct {
	Version   int          `json:"version,omitempty"`
	States    []PriceState `json:"states"`
	NextCheck time.Time    `json:"-"`
}

// The adapter exports customer-facing prices only, never account costs or keys.
type PriceSnapshotProvider interface {
	PriceSnapshot(context.Context, time.Time) (PriceSnapshot, error)
}

func (s *Service) SetPriceSnapshotProvider(source PriceSnapshotProvider) { s.priceSource = source }

func validatePriceNotifications(c Config) error {
	if len(c.PriceNotifications.Groups) > 100 {
		return errors.New("价格通知群最多 100 个")
	}
	for _, group := range c.PriceNotifications.Groups {
		if !c.allows(group) {
			return errors.New("价格通知群必须在允许使用的群列表中")
		}
	}
	return nil
}

func priceNotificationGroups(c Config) []string {
	groups := c.PriceNotifications.Groups
	if len(groups) == 0 {
		groups = c.AllowedGroups
	}
	out := []string{}
	seen := map[string]bool{}
	for _, g := range groups {
		if c.allows(g) && !c.groupPolicy(g).DisableNotifications && !seen[g] {
			out = append(out, g)
			seen[g] = true
		}
	}
	sort.Strings(out)
	return out
}

func (s *Service) resetPriceNotifications(ctx context.Context, tx *sql.Tx, old, current Config) error {
	if !old.PriceNotifications.Enabled && !current.PriceNotifications.Enabled {
		return nil
	}
	if old.Enabled == current.Enabled && old.AppID == current.AppID && old.Sandbox == current.Sandbox &&
		old.PriceNotifications.Enabled == current.PriceNotifications.Enabled && old.PriceNotifications.Scheduled == current.PriceNotifications.Scheduled &&
		slices.Equal(priceNotificationGroups(old), priceNotificationGroups(current)) {
		return nil
	}
	// Configuration changes establish a new baseline; never broadcast changes made
	// while disabled, or deliver old-environment messages to newly selected groups.
	if _, err := tx.ExecContext(ctx, `DELETE FROM qq_bot_price_snapshots WHERE app_id IN ($1,$2)`, old.AppID, current.AppID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE qq_bot_jobs SET status='cancelled',error='通知设置已变更',finished_at=NOW(),payload='{}' WHERE app_id IN ($1,$2) AND event_type=$3 AND status='queued'`, old.AppID, current.AppID, priceNotificationEvent)
	if err != nil || !current.Enabled || !current.PriceNotifications.Enabled || s.priceSource == nil {
		return err
	}
	// Capture the baseline before enabling returns, so a price edit immediately
	// after saving the toggle is not swallowed by the first background scan.
	now := time.Now()
	snapshot, err := s.priceSource.PriceSnapshot(ctx, now)
	if err != nil {
		return fmt.Errorf("初始化价格通知失败：%w", err)
	}
	next := now.Add(15 * time.Second)
	if current.PriceNotifications.Scheduled && snapshot.NextCheck.After(now) && snapshot.NextCheck.Before(next) {
		next = snapshot.NextCheck
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_price_snapshots(app_id,snapshot,next_check_at) VALUES($1,$2,$3)`, current.AppID, string(raw), next)
	return err
}

func (s *Service) startPriceMonitor(ctx context.Context) {
	if s.priceSource == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		next := time.Time{}
		for {
			now := time.Now()
			if !now.Before(next) {
				scanCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				var err error
				next, err = s.checkPriceChanges(scanCtx, now)
				cancel()
				if err != nil && ctx.Err() == nil {
					slog.Warn("qqbot price notification scan failed", "error", err)
					next = now.Add(15 * time.Second)
				}
			}
			// Wake at the actual boundary, including sub-second remainders. A
			// fixed one-second tick can miss short periods after a slow scan.
			delay := time.Until(next)
			if delay < 10*time.Millisecond {
				delay = 10 * time.Millisecond
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			case <-s.priceWake:
				timer.Stop()
				next = time.Time{}
			}
		}
	}()
}

func (s *Service) wakePriceMonitor() {
	select {
	case s.priceWake <- struct{}{}:
	default:
	}
}

func (s *Service) checkPriceChanges(ctx context.Context, now time.Time) (time.Time, error) {
	next := now.Add(15 * time.Second)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return next, err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := s.lockConfig(ctx, tx)
	if err != nil {
		return next, err
	}
	if !c.Enabled || !c.PriceNotifications.Enabled || s.priceSource == nil || len(priceNotificationGroups(c)) == 0 {
		return next, nil
	}
	var locked bool
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(71427107)`).Scan(&locked); err != nil || !locked {
		return now.Add(time.Second), err
	}
	var raw []byte
	var due time.Time
	err = tx.QueryRowContext(ctx, `SELECT snapshot,next_check_at FROM qq_bot_price_snapshots WHERE app_id=$1`, c.AppID).Scan(&raw, &due)
	initial := errors.Is(err, sql.ErrNoRows)
	if err != nil && !initial {
		return next, err
	}
	if !initial && now.Before(due) {
		return due, nil
	}
	current, err := s.priceSource.PriceSnapshot(ctx, now)
	if err != nil {
		return next, err
	}
	if c.PriceNotifications.Scheduled && current.NextCheck.After(now) && current.NextCheck.Before(next) {
		next = current.NextCheck
	}
	if !initial {
		var previous PriceSnapshot
		if err = json.Unmarshal(raw, &previous); err != nil {
			return next, err
		}
		chunks := formatPriceChanges(previous, current, c.PriceNotifications.Scheduled)
		batchID, sequence := randomID(), 0
		for _, group := range priceNotificationGroups(c) {
			for index, chunk := range chunks {
				// created_at is the transaction timestamp. Ordered IDs preserve
				// continuation order when several chunks share that timestamp.
				id := fmt.Sprintf("%s-%08d", batchID, sequence)
				sequence++
				body, err := json.Marshal(map[string]any{"text": chunk, "sandbox": c.Sandbox, "continuation": index > 0})
				if err != nil {
					return next, err
				}
				if _, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,payload,kind) VALUES($1,$2,$1,$3,$4,$5,'price_notification')`, id, c.AppID, priceNotificationEvent, group, string(body)); err != nil {
					return next, err
				}
			}
		}
	}
	raw, err = json.Marshal(current)
	if err != nil {
		return next, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_price_snapshots(app_id,snapshot,next_check_at) VALUES($1,$2,$3) ON CONFLICT(app_id) DO UPDATE SET snapshot=EXCLUDED.snapshot,next_check_at=EXCLUDED.next_check_at,updated_at=NOW()`, c.AppID, string(raw), next)
	if err != nil {
		return next, err
	}
	return next, tx.Commit()
}

func formatPriceChanges(previous, current PriceSnapshot, scheduled bool) []string {
	// Snapshot schema changes establish a fresh baseline instead of reporting
	// every newly tracked field as a customer-facing configuration change.
	if previous.Version != current.Version {
		return nil
	}
	old := map[string]PriceState{}
	for _, state := range previous.States {
		old[state.Key] = state
	}
	states := append([]PriceState(nil), current.States...)
	present := map[string]bool{}
	for _, state := range current.States {
		present[state.Key] = true
	}
	for _, state := range previous.States {
		if !present[state.Key] {
			states = append(states, state)
		}
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Key < states[j].Key })
	var chunks []string
	for _, state := range states {
		before, ok := old[state.Key]
		// New entities establish a baseline; existing entities include disabled
		// ones so deactivation cannot be mistaken for deletion.
		if !ok {
			continue
		}
		lines := []string{}
		onlyScheduled := false
		if !present[state.Key] {
			lines = append(lines, "已删除")
		} else {
			from, to := priceStateStatus(before), priceStateStatus(state)
			if from != to {
				lines = append(lines, "状态："+priceStatusLabel(from)+" → "+priceStatusLabel(to))
			}
			// Keep inactive prices as a baseline. Re-enabling reports the status
			// once rather than replaying price edits made while disabled.
			if from == "active" && to == "active" {
				changes, timed := priceFieldChanges(before, state, scheduled)
				onlyScheduled = len(lines) == 0 && timed
				lines = append(lines, changes...)
			}
		}
		if len(lines) == 0 {
			continue
		}
		chunks = append(chunks, formatPriceNotification(state, lines, onlyScheduled)...)
	}
	return chunks
}

func priceStateStatus(state PriceState) string {
	// Snapshots written before status tracking contained active entities only.
	if state.Status == "" {
		return "active"
	}
	return state.Status
}

func priceStatusLabel(status string) string {
	switch status {
	case "active":
		return "启用"
	case "disabled":
		return "停用"
	default:
		return replyPlainLabel(limitText(status, 80))
	}
}

func priceFieldChanges(before, state PriceState, scheduled bool) ([]string, bool) {
	prev := map[string]PriceField{}
	fields := map[string]PriceField{}
	for _, field := range before.Fields {
		prev[field.Key] = field
		fields[field.Key] = field
	}
	for _, field := range state.Fields {
		fields[field.Key] = field
	}
	after := map[string]PriceField{}
	for _, field := range state.Fields {
		after[field.Key] = field
	}
	baseBefore, hadBase := prev["01_text"]
	baseAfter, hasBase := after["01_text"]
	baseRateChanged := hadBase != hasBase || baseBefore.Value != baseAfter.Value || baseBefore.Unit != baseAfter.Unit
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := []string{}
	onlyScheduled := true
	model := ""
	for _, key := range keys {
		field := fields[key]
		if field.Scheduled && !scheduled {
			continue
		}
		a, had := prev[key]
		b, has := after[key]
		if had && has && a.Value == b.Value && a.Unit == b.Unit {
			continue
		}
		if state.Kind == "group" && key == "05_current" {
			// A base-rate edit also changes the derived rate. Report it once;
			// keep derived-only changes for scheduled peak transitions.
			if baseRateChanged {
				continue
			}
			field.Label = "倍率"
		}
		if !field.Scheduled {
			onlyScheduled = false
		}
		if field.Model != "" && field.Model != model {
			model = field.Model
			lines = append(lines, "模型："+replyPlainLabel(limitText(model, 180)))
		}
		from, to := "未配置", "已移除"
		if had {
			from = a.Value
		}
		if has {
			to = b.Value
		}
		line := replyPlainLabel(limitText(field.Label, 180)) + "：" + replyPlainLabel(limitText(priceNotificationValue(from), 180)) + " → " + replyPlainLabel(limitText(priceNotificationValue(to), 180))
		if field.Unit != "" {
			line += " / " + replyPlainLabel(field.Unit)
		}
		lines = append(lines, line)
	}
	return lines, onlyScheduled
}

func priceNotificationValue(value string) string {
	switch value {
	case "未设置（沿用计费默认值）":
		return "默认"
	case "跟随文字基础倍率", "跟随基础倍率":
		return "跟随倍率"
	default:
		return value
	}
}

func formatPriceNotification(state PriceState, lines []string, onlyScheduled bool) []string {
	noun := "渠道"
	if state.Kind == "group" {
		noun = "分组"
	}
	header := noun + "：" + replyPlainLabel(limitText(state.Name, 120))
	if onlyScheduled {
		header += "（定时切换）"
	}
	// Preserve every changed field; repeat the subject on continuation messages.
	var chunks []string
	chunk := header
	activeModel := ""
	for _, line := range lines {
		line = limitText(line, 900)
		if strings.HasPrefix(line, "模型：") {
			activeModel = line
		}
		if len([]rune(chunk+"\n"+line)) > 1750 && chunk != header {
			chunks = append(chunks, chunk)
			chunk = header
			if activeModel != "" && line != activeModel {
				chunk += "\n" + activeModel
			}
		}
		chunk += "\n" + line
	}
	chunks = append(chunks, chunk)
	return chunks
}

func (s *Service) deliverPriceNotification(parent context.Context, c Config, j Job) {
	if c.NotificationQuietHours.active(time.Now()) {
		s.finish(j, "skipped", "通知静默时段，跳过且不补发")
		return
	}
	if !allowsNotification(c, j) {
		s.finish(j, "cancelled", "通知已关闭或通知群已移除")
		return
	}
	if time.Since(j.CreatedAt) > time.Hour {
		s.finish(j, "expired", "通知排队超过 1 小时，未发送过期通知")
		return
	}
	var body struct {
		Text         string `json:"text"`
		Sandbox      bool   `json:"sandbox"`
		Continuation bool   `json:"continuation"`
	}
	if json.Unmarshal(j.Payload, &body) != nil || body.Text == "" || body.Sandbox != c.Sandbox {
		s.finish(j, "cancelled", "通知内容无效或 QQ 环境已变更")
		return
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	// QQ permits one @everyone message per group each day. A batch may contain
	// multiple changed subjects or chunks; mention only its first message.
	if body.Continuation {
		c.PriceNotifications.MentionAll = false
		c.HealthNotifications.MentionAll = false
	}
	err := s.sendPriceNotification(ctx, c, j, body.Text)
	if err != nil {
		s.finish(j, "delivery_failed", err.Error())
		return
	}
	s.finish(j, "completed", "")
}

func containsPriceGroup(c Config, group string) bool {
	for _, g := range priceNotificationGroups(c) {
		if g == group {
			return true
		}
	}
	return false
}

func (s *Service) sendPriceNotification(ctx context.Context, c Config, j Job, text string) error {
	if !c.Enabled || c.AppID != j.AppID || !allowsNotification(c, j) || c.NotificationQuietHours.active(time.Now()) {
		return ErrDisabled
	}
	switch j.EventType {
	case priceNotificationEvent:
		if c.PriceNotifications.MentionAll {
			text = "<qqbot-at-everyone />\n" + text
		}
	case healthNotificationEvent:
		if c.HealthNotifications.MentionAll {
			text = "<qqbot-at-everyone />\n" + text
		}
	case rankingNotificationEvent:
		text = `<qqbot-at-user id="` + html.EscapeString(j.MemberID) + `" />` + "\n" + text
	}
	if len([]rune(text)) > 1800 {
		return errors.New("通知内容过长")
	}
	// Deliberately separate from passive replies: no fabricated msg_id, event_id
	// or AI call. Unknown delivery outcomes are never auto-replayed.
	_, err := s.qqRequest(ctx, c, "/v2/groups/"+url.PathEscape(j.GroupID)+"/messages", map[string]any{
		"msg_type": 2, "markdown": map[string]string{"content": text},
	})
	if err != nil {
		return fmt.Errorf("通知发送失败：%w", err)
	}
	return nil
}

func allowsNotification(c Config, j Job) bool {
	if !c.allows(j.GroupID) {
		return false
	}
	switch j.EventType {
	case priceNotificationEvent:
		return c.PriceNotifications.Enabled && containsPriceGroup(c, j.GroupID)
	case healthNotificationEvent:
		return c.HealthNotifications.Enabled && slices.Contains(healthNotificationGroups(c), j.GroupID)
	case rankingNotificationEvent:
		return j.MemberID != "" && !c.groupPolicy(j.GroupID).DisableNotifications
	}
	return false
}
