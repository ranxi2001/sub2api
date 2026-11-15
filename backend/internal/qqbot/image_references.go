package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

const maxIncomingImageMessages = 100

func quotedImageAttachments(e Event) []Attachment {
	var attachments []Attachment
	remaining := 32
	var visit func([]MessageElement, int, bool)
	visit = func(elements []MessageElement, depth int, quoted bool) {
		if depth > 4 {
			return
		}
		for _, element := range elements {
			if remaining == 0 {
				return
			}
			remaining--
			isQuote := quoted || element.MessageType == 103
			if isQuote {
				attachments = append(attachments, element.Attachments...)
			}
			visit(element.MsgElements, depth+1, isQuote)
		}
	}
	visit(e.MsgElements, 0, e.MessageType == 103)
	return imageAttachments(attachments)
}

// Only this message's own attachments belong to its ID. A nested quote must
// never overwrite the identity of the picture being quoted.
func ownImageAttachments(e Event) []Attachment {
	attachments := append([]Attachment(nil), e.Attachments...)
	remaining := 32
	var visit func([]MessageElement, int)
	visit = func(elements []MessageElement, depth int) {
		if depth > 4 {
			return
		}
		for _, element := range elements {
			if remaining == 0 {
				return
			}
			remaining--
			if element.MessageType == 103 {
				continue
			}
			attachments = append(attachments, element.Attachments...)
			visit(element.MsgElements, depth+1)
		}
	}
	if e.MessageType != 103 {
		visit(e.MsgElements, 0)
	}
	return imageAttachments(attachments)
}

func (s *Service) rememberIncomingImages(ctx context.Context, c Config, e Event) error {
	if !c.ImageEnabled || s.db == nil || e.Author.Bot {
		return nil
	}
	if cmd, _ := command(e.Content, c.CustomCommands...); cmd != "" {
		return nil
	}
	images := ownImageAttachments(e)
	if len(images) == 0 || len(images) > maxInputImages {
		return nil
	}
	for i := range images {
		if strings.HasPrefix(images[i].URL, "//") {
			images[i].URL = "https:" + images[i].URL
		}
		u, err := url.Parse(images[i].URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(images[i].URL) > 4096 || len(images[i].ContentType) > 128 || images[i].Size > 10<<20 {
			return nil
		}
	}
	raw, err := json.Marshal(images)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,292))`, c.AppID+":"+e.GroupID); err != nil {
		return err
	}
	// Duplicate events never extend the retention window.
	if _, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_incoming_images(app_id,group_id,message_id,ref_idx,attachments)
 VALUES($1,$2,$3,$4,$5) ON CONFLICT(app_id,group_id,message_id) DO UPDATE
 SET ref_idx=CASE WHEN qq_bot_incoming_images.ref_idx='' THEN EXCLUDED.ref_idx ELSE qq_bot_incoming_images.ref_idx END`, c.AppID, e.GroupID, e.ID, e.MessageScene.value("msg_idx"), string(raw)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM qq_bot_incoming_images WHERE app_id=$1 AND group_id=$2 AND
 (created_at<NOW()-INTERVAL '24 hours' OR message_id IN
 (SELECT message_id FROM qq_bot_incoming_images WHERE app_id=$1 AND group_id=$2 ORDER BY created_at DESC,message_id DESC OFFSET $3))`, c.AppID, e.GroupID, maxIncomingImageMessages); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) referencedImageSources(ctx context.Context, c Config, e Event, ref *botMessageReference) ([]byte, []Attachment, error) {
	if s.db == nil || e.referenceID() == "" {
		return nil, nil, nil
	}
	if ref != nil {
		if !ref.Image || ref.ImageJobID == "" {
			return nil, nil, nil
		}
		var data []byte
		err := s.db.QueryRowContext(ctx, `SELECT result_image FROM qq_bot_jobs
 WHERE id=$1 AND app_id=$2 AND group_id=$3 AND created_at>NOW()-INTERVAL '24 hours'`, ref.ImageJobID, c.AppID, e.GroupID).Scan(&data)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, errors.New("引用图片读取失败，请重新发送原图。")
		}
		return data, nil, nil
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT attachments FROM qq_bot_incoming_images
 WHERE app_id=$1 AND group_id=$2 AND (message_id=$3 OR ref_idx=$3)
 AND created_at>NOW()-INTERVAL '24 hours' ORDER BY created_at DESC LIMIT 1`, c.AppID, e.GroupID, e.referenceID()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, errors.New("引用图片读取失败，请重新发送原图。")
	}
	var images []Attachment
	if json.Unmarshal(raw, &images) != nil || len(images) > maxInputImages {
		return nil, nil, errors.New("引用图片记录无效，请重新发送原图。")
	}
	return nil, images, nil
}
