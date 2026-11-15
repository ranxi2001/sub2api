package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

func (scene MessageScene) value(key string) string {
	for _, item := range scene.Ext {
		name, value, ok := strings.Cut(item, "=")
		if ok && name == key && len(value) <= 1024 {
			return value
		}
	}
	return ""
}

func (e Event) referenceID() string {
	if id := e.MessageScene.value("ref_msg_idx"); id != "" {
		return id
	}
	if len(e.MessageReference.MessageID) <= 1024 {
		return e.MessageReference.MessageID
	}
	return ""
}

type botMessageReference struct {
	Content    string
	Image      bool
	Shareable  bool
	ImageJobID string
}

func (s *Service) referencedMessage(ctx context.Context, c Config, group, id string) (*botMessageReference, error) {
	if id == "" || s.db == nil {
		return nil, nil
	}
	var ref botMessageReference
	err := s.db.QueryRowContext(ctx, `SELECT content,is_image,shareable,image_job_id FROM qq_bot_message_references
 WHERE app_id=$1 AND group_id=$2 AND (ref_idx=$3 OR message_id=$3)
	AND created_at>NOW()-INTERVAL '24 hours' ORDER BY created_at DESC LIMIT 1`, c.AppID, group, id).Scan(&ref.Content, &ref.Image, &ref.Shareable, &ref.ImageJobID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("引用消息查询失败")
	}
	return &ref, nil
}

func (s *Service) rememberSentMessage(ctx context.Context, c Config, j Job, raw []byte, content string, image, shareable bool) error {
	if s.db == nil {
		return nil
	}
	var sent struct {
		ID  string `json:"id"`
		Ext struct {
			Reference string `json:"ref_idx"`
		} `json:"ext_info"`
	}
	if json.Unmarshal(raw, &sent) != nil {
		return nil // Successful legacy QQ responses can omit message metadata.
	}
	if sent.ID == "" {
		sent.ID = sent.Ext.Reference
	}
	if sent.ID == "" {
		return nil
	}
	imageJobID := ""
	if image {
		imageJobID = j.ID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO qq_bot_message_references(app_id,group_id,message_id,ref_idx,content,is_image,shareable,image_job_id,source_job_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(app_id,group_id,message_id)
 DO UPDATE SET ref_idx=EXCLUDED.ref_idx,content=EXCLUDED.content,is_image=EXCLUDED.is_image,shareable=EXCLUDED.shareable,image_job_id=EXCLUDED.image_job_id,source_job_id=EXCLUDED.source_job_id,created_at=NOW()`,
		c.AppID, j.GroupID, sent.ID, sent.Ext.Reference, content, image, shareable, imageJobID, j.ID)
	if err != nil {
		return errors.New("消息已发送，但引用记录保存失败")
	}
	if shareable {
		id := sent.ID
		if sent.Ext.Reference != "" {
			id = sent.Ext.Reference
		}
		entry := groupHistoryEntry{Speaker: "机器人", ID: groupHistoryID(c.AppID, j.GroupID, id), ReplyTo: groupHistoryID(c.AppID, j.GroupID, j.MessageID), Text: limitText(content, 1200)}
		member := j.MemberID
		if j.Kind == "ambient_chat" && j.TriggerReason != "followup" {
			// Shared ambient answers can mention other participants. Never reuse
			// those answers as personal conversation history.
			member = ""
		}
		if err := s.appendGroupHistory(ctx, c, j.GroupID, member, "assistant:"+sent.ID, entry.message("assistant"), j.CreatedAt, j.MessageID); err != nil {
			return errors.New("消息已发送，但群历史保存失败")
		}
	}
	return nil
}
