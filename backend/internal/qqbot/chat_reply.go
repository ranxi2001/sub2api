package qqbot

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// chatReply handles user-facing chat only. Autonomous decisions use chat
// directly because their action/text JSON is an intentional output protocol.
func (s *Service) chatReply(ctx context.Context, c Config, messages []Message) (string, int64, int64, error) {
	text, in, out, err := s.chat(ctx, c, messages)
	if err != nil {
		return text, in, out, err
	}
	text = normalizeChatReply(text)
	if strings.TrimSpace(text) == "" {
		return "", in, out, errors.New("模型没有返回文本")
	}
	return text, in, out, nil
}

// Only unwrap our complete bot-history envelope, never arbitrary JSON, code
// examples embedded in prose, or another member's message. Run before saving
// or splitting the reply so delivery, quoted replies and history all agree.
func normalizeChatReply(text string) string {
	if body, ok := groupHistoryReplyText(text); ok {
		return body
	}
	return text
}

func groupHistoryReplyText(text string) (string, bool) {
	raw := strings.TrimSpace(text)
	for _, fence := range []string{"```", "~~~"} {
		if !strings.HasPrefix(raw, fence) {
			continue
		}
		header, rest, ok := strings.Cut(raw, "\n")
		header = strings.TrimSpace(header)
		if !ok || (header != fence && !strings.EqualFold(header, fence+"json")) || !strings.HasSuffix(rest, fence) {
			return "", false
		}
		raw = strings.TrimSpace(strings.TrimSuffix(rest, fence))
		break
	}
	var entry struct {
		Speaker  string  `json:"speaker"`
		Nickname string  `json:"nickname,omitempty"`
		ID       string  `json:"id,omitempty"`
		ReplyTo  string  `json:"reply_to,omitempty"`
		Text     *string `json:"text"`
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&entry) != nil || entry.Speaker != "机器人" || entry.Text == nil || (entry.ID == "" && entry.ReplyTo == "") {
		return "", false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return "", false
	}
	for _, id := range []string{entry.ID, entry.ReplyTo} {
		if id == "" {
			continue
		}
		if len(id) != 16 {
			return "", false
		}
		if _, err := hex.DecodeString(id); err != nil {
			return "", false
		}
	}
	return strings.TrimSpace(*entry.Text), true
}

// Keep the stored envelope for ordering and reference metadata, but do not
// teach the model to emit it by using it as an assistant response example.
func groupHistoryModelMessage(m Message) Message {
	if m.Role == "assistant" {
		if body, ok := groupHistoryReplyText(m.Content); ok {
			m.Content = normalizeChatReply(body) // Also repair legacy leaked replies when read.
		}
	}
	return m
}
