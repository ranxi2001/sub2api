package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var siteQueryPageFollowup = regexp.MustCompile("^(?:查看)?第?[[:space:]]*([0-9]{1,5})[[:space:]]*页$")
var siteQueryModelFollowup = regexp.MustCompile("^(?:换成|换为|改查|查一下|查询)[[:space:]]*([A-Za-z0-9][A-Za-z0-9._:/-]{0,199})(?:的价格|价格)?$")

// Reuse only parameters, never an old result or authorization identity.
func siteQueryFollowup(text string, previous SiteQueryDecision) (*siteQuery, bool) {
	text = strings.TrimSpace(mentionPrefix.ReplaceAllString(strings.TrimSpace(text), ""))
	text = strings.TrimRightFunc(text, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune("。.!！?？", r) })
	query := siteQuery{Kind: previous.Kind, Model: previous.Model, Activity: previous.Activity, Page: previous.Page}
	if query.Page == 0 {
		query.Page = 1
	}
	switch text {
	case "下一页", "下页":
		query.Page++
	case "上一页", "上页":
		if query.Page <= 1 {
			return nil, false
		}
		query.Page--
	case "再查一次", "重新查询", "刷新查询", "再查一下":
	default:
		if m := siteQueryPageFollowup.FindStringSubmatch(text); m != nil {
			query.Page, _ = strconv.Atoi(m[1])
		} else if m := siteQueryModelFollowup.FindStringSubmatch(text); m != nil && query.Kind == "model_price" {
			query.Model = m[1]
			query.Page = 1
		} else {
			return nil, false
		}
	}
	if query.Page < 1 || query.validate() != nil {
		return nil, false
	}
	return &query, true
}

// Only the immediately preceding interaction from this member and group can
// continue a delivered query, for ten minutes. Other chats break the chain.
func (s *Service) resolveSiteQueryFollowup(ctx context.Context, c Config, j Job, text string) (*siteQuery, error) {
	if s.db == nil || j.MemberID == "" || j.GroupID == "" {
		return nil, nil
	}
	if _, ok := siteQueryFollowup(text, SiteQueryDecision{Kind: "model_price", Model: "probe", Page: 2}); !ok {
		return nil, nil
	}
	at := j.CreatedAt
	if at.IsZero() {
		at = time.Now()
	}
	var raw []byte
	var status string
	err := s.db.QueryRowContext(ctx, "SELECT site_query,status FROM qq_bot_jobs WHERE app_id=$1 AND group_id=$2 AND member_id=$3 AND id<>$4 AND event_type IN ('GROUP_AT_MESSAGE_CREATE','GROUP_MESSAGE_CREATE','INTERACTION_CREATE') AND created_at<=$5 AND created_at>$5::timestamptz-INTERVAL '10 minutes' ORDER BY created_at DESC,id DESC LIMIT 1", c.AppID, j.GroupID, j.MemberID, j.ID, at).Scan(&raw, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("查询上下文读取失败，请发送完整查询问题。")
	}
	if status != "completed" {
		return nil, nil
	}
	var previous SiteQueryDecision
	if json.Unmarshal(raw, &previous) != nil || previous.Status == "" {
		return nil, nil
	}
	query, _ := siteQueryFollowup(text, previous)
	return query, nil
}

func (s *Service) recordSiteQuery(ctx context.Context, j *Job, decision SiteQueryDecision) error {
	raw, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	j.SiteQuery = raw
	j.DecisionReason = "site_query:" + decision.Kind
	_, err = s.db.ExecContext(ctx, "UPDATE qq_bot_jobs SET decision_reason=$2,site_query=$3 WHERE id=$1", j.ID, j.DecisionReason, string(raw))
	return err
}
