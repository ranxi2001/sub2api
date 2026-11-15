package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
)

// GroupInfo contains display-only metadata. A name never grants group access.
type GroupInfo struct {
	AppID             string `json:"app_id"`
	Sandbox           bool   `json:"sandbox"`
	GroupID           string `json:"group_id"`
	Name              string `json:"name"`
	Status            string `json:"status"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
}
type groupInfoEntry struct {
	info    GroupInfo
	expires time.Time
}
type groupInfoCache struct {
	once             sync.Once
	gate             chan struct{}
	entries          map[string]groupInfoEntry
	requests         []time.Time
	permissionKey    string
	permissionUntil  time.Time
	rateLimitedUntil time.Time
}

func validGroupInfoID(id string) bool {
	return id != "" && len(id) <= 128 && !strings.ContainsAny(id, "/\\?#") && strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func (s *Service) GroupInfo(ctx context.Context, appID, groupID string) (GroupInfo, error) {
	if !validGroupInfoID(groupID) {
		return GroupInfo{}, errors.New("群 OpenID 格式不正确")
	}
	c, err := s.Config(ctx)
	if err != nil {
		return GroupInfo{}, err
	}
	if appID == "" || c.AppID != appID {
		return GroupInfo{}, errors.New("机器人 AppID 已变更，请刷新后重试")
	}
	if !c.allows(groupID) {
		var known bool
		err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM qq_bot_groups WHERE app_id=$1 AND group_id=$2)`, c.AppID, groupID).Scan(&known)
		if err != nil {
			return GroupInfo{}, err
		}
		if !known {
			return GroupInfo{}, errors.New("群不在当前机器人的允许或已识别列表中")
		}
	}
	return s.loadGroupInfo(ctx, c, groupID), nil
}

// QQ's group-info endpoint is whitelist-only and limited to 30 QPM. Cache both
// success and failure, serialize requests, and stop probing an app after denial.
// Keep this lookup separate from runtime polling and message processing.
func (s *Service) loadGroupInfo(ctx context.Context, c Config, groupID string) GroupInfo {
	out := GroupInfo{AppID: c.AppID, Sandbox: c.Sandbox, GroupID: groupID, Status: "unavailable"}
	if c.AppID == "" || c.AppSecret == "" {
		out.Status = "unconfigured"
		return out
	}
	identity, _ := json.Marshal([]any{c.AppID, c.Sandbox, c.AppSecret})
	appKey := codeHash(string(identity))
	key := appKey + ":" + groupID
	cache := &s.groupInfo
	cache.once.Do(func() { cache.gate = make(chan struct{}, 1) })
	select {
	case cache.gate <- struct{}{}:
		defer func() { <-cache.gate }()
	case <-ctx.Done():
		return out
	}
	now := time.Now()
	if entry, ok := cache.entries[key]; ok && now.Before(entry.expires) {
		return entry.info
	}
	if cache.permissionKey == appKey && now.Before(cache.permissionUntil) {
		out.Status = "permission_required"
		return out
	}
	if ctx.Err() != nil {
		return out
	}
	if now.Before(cache.rateLimitedUntil) {
		out.Status = "rate_limited"
		out.RetryAfterSeconds = int(time.Until(cache.rateLimitedUntil).Seconds()) + 1
		return out
	}
	for len(cache.requests) > 0 && !cache.requests[0].After(now.Add(-time.Minute)) {
		cache.requests = cache.requests[1:]
	}
	if len(cache.requests) >= 30 {
		out.Status = "rate_limited"
		out.RetryAfterSeconds = int(time.Until(cache.requests[0].Add(time.Minute)).Seconds()) + 1
		return out
	}
	cache.requests = append(cache.requests, now)
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := s.qqRequestMethod(requestCtx, c, http.MethodGet, "/v2/groups/"+url.PathEscape(groupID)+"/info", nil)
	var body struct {
		GroupID string `json:"group_openid"`
		Name    string `json:"group_name"`
		Code    int    `json:"code"`
	}
	parsed := json.Unmarshal(raw, &body) == nil
	var qqErr *qqRequestError
	if errors.As(err, &qqErr) {
		body.Code = qqErr.Code
		if qqErr.StatusCode == http.StatusTooManyRequests {
			cache.rateLimitedUntil = time.Now().Add(time.Minute)
			out.Status, out.RetryAfterSeconds = "rate_limited", 60
			return out
		}
	}
	ttl := 5 * time.Minute
	if body.Code == 11253 {
		out.Status = "permission_required"
		cache.permissionKey, cache.permissionUntil = appKey, now.Add(ttl)
	} else if err == nil && parsed && body.Code == 0 && (body.GroupID == "" || body.GroupID == groupID) && strings.TrimSpace(body.Name) != "" {
		out.Name, out.Status = limitText(strings.TrimSpace(body.Name), 100), "available"
		ttl = time.Hour
	}
	if ctx.Err() != nil {
		return out
	}
	if cache.entries == nil {
		cache.entries = map[string]groupInfoEntry{}
	}
	for k, v := range cache.entries {
		if !now.Before(v.expires) {
			delete(cache.entries, k)
		}
	}
	if len(cache.entries) >= 512 {
		for k := range cache.entries {
			delete(cache.entries, k)
			break
		}
	}
	cache.entries[key] = groupInfoEntry{info: out, expires: now.Add(ttl)}
	return out
}
