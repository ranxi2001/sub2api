package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var accountOpsHTTPClient = newSSRFSafeHTTPClient(12 * time.Second)

func opsSign(key, msg string) string {
	h := hmac.New(sha256.New, []byte(key))
	_, _ = h.Write([]byte(msg))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
func (s *AccountOpsService) sendRobot(ctx context.Context, w AccountOpsWebhook, message accountOpsMessage) error {
	if !s.EncryptionKeyConfigured() {
		return errors.New("robot encryption unavailable")
	}
	raw, err := s.encryptor.Decrypt(w.urlCipher)
	if err != nil {
		return errors.New("robot credential decryption failed")
	}
	if err = validateAccountOpsURL(w.Provider, raw); err != nil {
		return err
	}
	secret := ""
	if w.secretCipher != "" {
		secret, err = s.encryptor.Decrypt(w.secretCipher)
		if err != nil {
			return errors.New("robot credential decryption failed")
		}
	}
	// Keep robot requests sequential with a minimum interval below provider's 20/minute ceiling.
	if w.Provider == "wecom" || w.Provider == "dingtalk" {
		s.robotMu.Lock()
		last := s.robotLast[raw]
		wait := time.Until(last.Add(3100 * time.Millisecond))
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				s.robotMu.Unlock()
				return errors.New("robot request cancelled")
			case <-timer.C:
			}
		}
		s.robotLast[raw] = time.Now()
		s.robotMu.Unlock()
	}
	now := time.Now()
	title := message.robotTitle
	if title == "" {
		title = message.title
	}
	payload := map[string]any{}
	switch w.Provider {
	case "custom":
		payload, err = accountOpsCustomPayload(opsTemplateValue(w.MessageTemplate), message)
		if err != nil {
			return errors.New("invalid webhook message template")
		}
	case "wecom":
		payload = map[string]any{"msgtype": "markdown", "markdown": map[string]any{"content": message.markdown}}
	case "dingtalk":
		payload = map[string]any{"msgtype": "markdown", "markdown": map[string]any{"title": title, "text": message.markdown}}
		if secret != "" {
			ts := strconv.FormatInt(now.UnixMilli(), 10)
			u, _ := url.Parse(raw)
			q := u.Query()
			q.Set("timestamp", ts)
			q.Set("sign", opsSign(secret, ts+"\n"+secret))
			u.RawQuery = q.Encode()
			raw = u.String()
		}
	case "feishu":
		color := message.color
		if color == "" {
			color = "orange"
		}
		payload = map[string]any{"msg_type": "interactive", "card": map[string]any{"header": map[string]any{"template": color, "title": map[string]any{"tag": "plain_text", "content": title}}, "elements": []any{map[string]any{"tag": "div", "text": map[string]any{"tag": "plain_text", "content": message.plain}}}}}
		if secret != "" {
			ts := strconv.FormatInt(now.Unix(), 10)
			payload["timestamp"] = ts
			payload["sign"] = opsSign(ts+"\n"+secret, "")
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("robot message encoding failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, raw, bytes.NewReader(body))
	if err != nil {
		return errors.New("robot request unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.robotClient
	if client == nil {
		client = accountOpsHTTPClient
	}
	guarded := *client
	guarded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := guarded.Do(req)
	if err != nil {
		return errors.New("robot request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if w.Provider == "custom" {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return errors.New("webhook HTTP failure")
		}
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("robot HTTP failure")
	}
	result, err := io.ReadAll(io.LimitReader(resp.Body, 16385))
	if err != nil || len(result) > 16384 {
		return errors.New("invalid robot response")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(result, &fields) != nil {
		return errors.New("invalid robot response")
	}
	key := "errcode"
	if w.Provider == "feishu" {
		key = "code"
		if _, ok := fields[key]; !ok {
			key = "StatusCode"
		}
	}
	var code *int
	v, ok := fields[key]
	if !ok || json.Unmarshal(v, &code) != nil || code == nil || *code != 0 {
		return errors.New("robot rejected message")
	}
	return nil
}
func escapeOpsMarkdown(raw string) string {
	raw = strings.ReplaceAll(raw, "\n", " ")
	raw = strings.ReplaceAll(raw, "\r", " ")
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "`", "\\`", "\\", "\\\\").Replace(raw)
}
func (s *AccountOpsService) TestWebhook(ctx context.Context, id string) error {
	s.settingsMu.Lock()
	c, err := s.loadConfig(ctx)
	s.settingsMu.Unlock()
	if err != nil {
		return err
	}
	for _, w := range c.Webhooks {
		if w.ID == id {
			send, stop := context.WithTimeout(ctx, 15*time.Second)
			defer stop()
			if rate, ok := s.repo.(interface {
				ReserveRobotTest(context.Context, string) error
			}); ok {
				identity, identityErr := s.robotIdentity(w)
				if identityErr != nil {
					return identityErr
				}
				if err = rate.ReserveRobotTest(send, identity); err != nil {
					return errors.New("robot test rate reservation failed")
				}
			}
			s.settingsMu.Lock()
			latest, latestErr := s.loadConfig(send)
			s.settingsMu.Unlock()
			if latestErr != nil {
				return latestErr
			}
			same := false
			for _, current := range latest.Webhooks {
				if current.ID == w.ID && current.Provider == w.Provider && current.revision == w.revision {
					same = true
					break
				}
			}
			if !same {
				return errors.New("saved robot destination changed during test")
			}
			message := s.compactRobotMessage(&AccountOpsEvent{AccountName: "测试账号", LastSeen: time.Now()}, "账号运维测试")
			return s.sendRobot(send, w, message)
		}
	}
	return errors.New("saved robot destination not found")
}

func (s *AccountOpsService) robotIdentity(w AccountOpsWebhook) (string, error) {
	if s.encryptor == nil {
		return "", errors.New("robot encryption unavailable")
	}
	raw, err := s.encryptor.Decrypt(w.urlCipher)
	if err != nil {
		return "", errors.New("robot credential decryption failed")
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:]), nil
}
