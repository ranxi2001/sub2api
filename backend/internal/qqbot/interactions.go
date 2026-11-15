package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const interactionCreate = "INTERACTION_CREATE"

type buttonInteraction struct {
	ID        string `json:"id"`
	Type      int    `json:"type"`
	Scene     string `json:"scene"`
	ChatType  *int   `json:"chat_type"`
	AppID     string `json:"application_id"`
	GroupID   string `json:"group_openid"`
	MemberID  string `json:"group_member_openid"`
	Timestamp string `json:"timestamp"`
	Data      struct {
		Type     int `json:"type"`
		Resolved struct {
			ButtonID   string `json:"button_id"`
			ButtonData string `json:"button_data"`
		} `json:"resolved"`
	} `json:"data"`
}

// Called only after the webhook's AppID and Ed25519 signature checks. Persist
// before acknowledging QQ's three-second callback deadline; the ordinary worker
// performs the command using the clicking member's identity and existing rules.
func (s *Service) enqueueInteraction(ctx context.Context, c Config, p Payload) error {
	ctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	var interaction buttonInteraction
	if err := json.Unmarshal(p.Data, &interaction); err != nil {
		return err
	}
	if interaction.Type != 11 || interaction.ID == "" || len(interaction.ID) > 1024 || (interaction.AppID != "" && interaction.AppID != c.AppID) {
		return nil
	}
	ack := func(code int) error {
		_, err := s.qqRequestMethod(ctx, c, http.MethodPut, "/interactions/"+url.PathEscape(interaction.ID), map[string]int{"code": code})
		return err
	}
	if !c.Enabled || !c.allows(interaction.GroupID) || interaction.GroupID == "" || len(interaction.GroupID) > 128 || interaction.MemberID == "" || len(interaction.MemberID) > 128 ||
		(interaction.Scene != "group" && (interaction.Scene != "" || interaction.ChatType == nil || *interaction.ChatType != 1)) || (interaction.ChatType != nil && *interaction.ChatType != 1) {
		return ack(4)
	}
	action, supported := strings.CutPrefix(interaction.Data.Resolved.ButtonData, helpActionPrefix)
	cmd, valid := helpActionCommand(c, action)
	if !supported || !valid || (interaction.Data.Type != 0 && interaction.Data.Type != 11) ||
		(interaction.Data.Resolved.ButtonID != "" && interaction.Data.Resolved.ButtonID != helpButtonID(action)) || p.ID == "" || len(p.ID) > 1024 {
		return ack(1)
	}
	createdAt := time.Now()
	if interaction.Timestamp != "" {
		at, err := time.Parse(time.RFC3339, interaction.Timestamp)
		if err != nil || time.Since(at) > 4*time.Minute || time.Until(at) > time.Minute {
			return ack(1)
		}
		if at.Before(createdAt) {
			createdAt = at
		}
	}
	e := Event{GroupID: interaction.GroupID, Content: cmd, HelpAction: action}
	e.Author.MemberID = interaction.MemberID
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(71427102)`); err != nil {
		return err
	}
	eventKey := interactionCreate + ":" + interaction.ID
	var duplicate bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM qq_bot_jobs WHERE app_id=$1 AND event_key=$2)`, c.AppID, eventKey).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate {
		// QQ accepts one acknowledgement per interaction; retries must neither
		// acknowledge again nor execute a second lottery draw/reward operation.
		return nil
	}
	var queued int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM qq_bot_jobs WHERE status='queued'`).Scan(&queued); err != nil {
		return err
	}
	if queued >= 200 {
		return errors.New("机器人队列已满")
	}
	// For interaction jobs, message_id stores the outer event ID, which QQ's
	// group send API requires as event_id. Never use the old help message ID.
	_, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,member_id,message_id,payload,created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, randomID(), c.AppID, eventKey, interactionCreate, interaction.GroupID, interaction.MemberID, p.ID, string(raw), createdAt)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return ack(0)
}
