package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const maxAutonomousQueue = 8
const maxAutonomousBatch = 4
const autonomousJobLifetime = 2 * time.Minute

// Only locally validated events enter a batch. Keep the latest QQ reply target,
// never mix speakers, and cap both burst duration and total content.
func mergeAutonomousEvents(previous, current Event) (Event, bool) {
	if previous.GroupID != current.GroupID || previous.Author.MemberID != current.Author.MemberID {
		return current, false
	}
	text := autonomousContentText(previous) + "\n" + autonomousContentText(current)
	images := append(ownImageAttachments(previous), ownImageAttachments(current)...)
	if len([]rune(text)) > 1500 || len(images) > maxInputImages {
		return current, false
	}
	current.Content, current.MsgElements, current.Attachments = text, nil, images
	return current, true
}

func coalesceAutonomous(ctx context.Context, tx *sql.Tx, c Config, e Event) (bool, error) {
	if c.Autonomous.MergeSeconds == 0 {
		return false, nil
	}
	var id string
	var payload, metadata []byte
	err := tx.QueryRowContext(ctx, `SELECT id,payload,autonomous_data FROM qq_bot_jobs q
 WHERE app_id=$1 AND group_id=$2 AND member_id=$3 AND kind='ambient_chat' AND status='queued'
 AND created_at>clock_timestamp()-($4*INTERVAL '1 second')
 AND NOT EXISTS(SELECT 1 FROM qq_bot_autonomous_seen s WHERE s.app_id=q.app_id AND s.group_id=q.group_id AND s.member_id=q.member_id AND s.ends_conversation AND s.created_at>q.created_at)
 ORDER BY created_at DESC,id DESC LIMIT 1 FOR UPDATE`, c.AppID, e.GroupID, e.Author.MemberID, c.Autonomous.MergeSeconds).Scan(&id, &payload, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var previous Event
	var data AutonomousExecution
	if json.Unmarshal(payload, &previous) != nil || json.Unmarshal(metadata, &data) != nil {
		return false, nil
	}
	if data.BatchMessages >= maxAutonomousBatch {
		return false, nil
	}
	merged, ok := mergeAutonomousEvents(previous, e)
	if !ok {
		return false, nil
	}
	data.BatchMessages = max(data.BatchMessages, 1) + 1
	data.MergedHistoryIDs = append(data.MergedHistoryIDs, groupHistoryID(c.AppID, e.GroupID, previous.ID))
	raw, err := json.Marshal(merged)
	if err != nil {
		return false, err
	}
	meta, err := json.Marshal(data)
	if err != nil {
		return false, err
	}
	nickname, preview := groupMemberHint(e, c.CustomCommands...)
	_, err = tx.ExecContext(ctx, `UPDATE qq_bot_jobs SET payload=$2,message_id=$3,autonomous_data=$4,member_nickname=$5,message_preview=$6 WHERE id=$1`, id, string(raw), e.ID, string(meta), nickname, preview)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE qq_bot_autonomous_seen SET job_id=$4 WHERE app_id=$1 AND group_id=$2 AND message_id=$3`, c.AppID, e.GroupID, e.ID, id)
	}
	return err == nil, err
}

func (s *Service) deferAutonomous(ctx context.Context, c Config, j *Job, reason string) error {
	// Waiting never consumes a model call. The queue timestamp is bounded by the
	// original job age, so repeated deferral cannot extend a reply indefinitely.
	result, err := s.db.ExecContext(ctx, `UPDATE qq_bot_jobs SET status='queued',started_at=NULL,decision_reason=$2,
 autonomous_ready_at=GREATEST(clock_timestamp()+INTERVAL '1 second',
 COALESCE((SELECT MAX(reply_reserved_at)+($3*INTERVAL '1 second') FROM qq_bot_jobs WHERE app_id=$4 AND group_id=$5 AND kind='ambient_chat'),'epoch'::timestamptz),
 COALESCE((SELECT last_reply_at+($3*INTERVAL '1 second') FROM qq_bot_autonomous_state WHERE app_id=$4 AND group_id=$5),'epoch'::timestamptz),
 COALESCE((SELECT MAX(reply_reserved_at)+($6*INTERVAL '1 second') FROM qq_bot_jobs WHERE app_id=$4 AND group_id=$5 AND member_id=$7 AND kind='ambient_chat'),'epoch'::timestamptz))
 WHERE id=$1 AND status='running' AND model_reserved_at IS NULL`, j.ID, reason, c.Autonomous.CooldownSeconds, c.AppID, j.GroupID, c.Autonomous.MemberCooldownSeconds, j.MemberID)
	if err == nil {
		n, countErr := result.RowsAffected()
		if countErr != nil {
			return countErr
		}
		if n != 1 {
			return errors.New("自主接话排队状态已改变")
		}
		j.Status = "deferred"
	}
	return err
}
