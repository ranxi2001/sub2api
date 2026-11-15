package qqbot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// ChatPreviewRequest deliberately accepts QQ identities, never a website user
// ID. Membership is checked against the current application's member directory.
type ChatPreviewRequest struct {
	GroupID  string `json:"group_id"`
	MemberID string `json:"member_id,omitempty"`
	Question string `json:"question"`
}

type ChatPreviewResult struct {
	Text             string             `json:"text"`
	Action           string             `json:"action"`
	InputTokens      int64              `json:"input_tokens"`
	OutputTokens     int64              `json:"output_tokens"`
	DurationMS       int64              `json:"duration_ms"`
	KnowledgeSources []KnowledgeSource  `json:"knowledge_sources"`
	Query            *SiteQueryDecision `json:"query,omitempty"`
	ChatExecution    *ChatExecution     `json:"chat_execution,omitempty"`
}

// Bound operator-funded trials per service. Entries exist only while a trial
// runs; concurrent requests fail immediately without another paid request.
var activeChatPreviews sync.Map

func validateChatPreviewRequest(req ChatPreviewRequest) error {
	for _, id := range []string{req.GroupID, req.MemberID} {
		if len(id) > 128 || strings.ContainsFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return errors.New("试问群或成员身份格式不正确")
		}
	}
	if req.GroupID == "" {
		return errors.New("请选择试问的群")
	}
	if strings.TrimSpace(req.Question) == "" || utf8.RuneCountInString(req.Question) > 6000 {
		return errors.New("试问内容长度须为 1～6000 字")
	}
	if cmd, arg := command(req.Question); cmd != "" && (cmd != "帮助" || arg != "") {
		return errors.New("试问仅支持自然语言或“帮助”，不执行签到、抽奖、绑定、转账等指令")
	}
	return nil
}

// PreviewChat exercises the same explicit-chat model routing and read-only site
// query providers as QQ messages, without enqueuing jobs, sending messages,
// reserving member quotas, loading conversations, or executing image actions.
func (s *Service) PreviewChat(ctx context.Context, req ChatPreviewRequest) (ChatPreviewResult, error) {
	started := time.Now()
	out := ChatPreviewResult{KnowledgeSources: []KnowledgeSource{}}
	req.Question = strings.TrimSpace(req.Question)
	if err := validateChatPreviewRequest(req); err != nil {
		return out, err
	}
	if _, active := activeChatPreviews.LoadOrStore(s, struct{}{}); active {
		return out, errors.New("已有试问正在运行，请等待完成后再试")
	}
	defer activeChatPreviews.Delete(s)
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	c, err := s.Config(ctx)
	if err != nil {
		return out, errors.New("读取试问配置失败，请稍后重试")
	}
	if !c.Enabled {
		return out, ErrDisabled
	}
	if !c.allows(req.GroupID) {
		return out, errors.New("试问群未在允许名单中")
	}
	if req.MemberID != "" {
		page, memberErr := s.ListMembers(ctx, MemberDirectoryFilter{AppID: c.AppID, GroupID: req.GroupID, MemberID: req.MemberID, Page: 1, PageSize: 1})
		if memberErr != nil {
			return out, errors.New("无法验证试问成员，请重新选择成员后重试")
		}
		if len(page.Items) != 1 || page.AppID != c.AppID || page.Items[0].GroupID != req.GroupID || page.Items[0].MemberID != req.MemberID {
			return out, errors.New("试问成员不在当前机器人的所选群成员目录中")
		}
	}
	cmd, _ := command(req.Question)
	if naturalHelpRequest(req.Question) || cmd == "帮助" {
		out.Action, out.Text = "menu", helpText(c)
		out.DurationMS = time.Since(started).Milliseconds()
		return out, nil
	}
	if c.ChatDisabled || c.groupPolicy(req.GroupID).DisableChat {
		return out, errors.New("当前群已停用 AI 聊天")
	}
	if c.DailyChatLimit <= 0 || c.TotalDailyChatLimit <= 0 || c.groupLimit(req.GroupID, "chat") <= 0 {
		return out, errors.New("聊天额度配置为零，请先检查成员、群及总额度配置")
	}
	if c.BaseURL == "" || c.APIKey == "" || c.ChatModel == "" {
		return out, errors.New("请先保存聊天连接和模型")
	}
	c.ImageEnabled, c.HistoryMessages = false, 0
	j := Job{AppID: c.AppID, GroupID: req.GroupID, MemberID: req.MemberID, CreatedAt: started}
	messages := s.historyForQuestion(ctx, c, j, req.Question)
	out.KnowledgeSources = siteKnowledgeSourcesForQuestion(c.SiteKnowledge, req.Question)
	if out.KnowledgeSources == nil {
		out.KnowledgeSources = []KnowledgeSource{}
	}
	messages = withChatCapabilities(messages, c, j)
	e := Event{ID: "admin-preview", GroupID: req.GroupID, Content: req.Question}
	e.Author.MemberID = req.MemberID
	messages = append(messages, groupUserMessage(c, e, Message{Role: "user", Content: req.Question}))
	turn, err := s.siteChatTurn(ctx, c, messages)
	out.InputTokens, out.OutputTokens = turn.InputTokens, turn.OutputTokens
	if turn.ChatExecution.Attempts > 0 {
		out.ChatExecution = &turn.ChatExecution
	}
	out.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		return out, errors.New("试问模型调用失败，请检查聊天连接及工具能力")
	}
	out.Action, out.Text = "chat", turn.Text
	if turn.SiteQuery != nil {
		result, queryErr := s.querySiteInfoDetailed(ctx, c, j, *turn.SiteQuery)
		if queryErr != nil {
			return out, errors.New("试问查询参数不正确，请调整问题后重试")
		}
		out.Action, out.Text, out.Query = "site_query", result.Text, &result.Decision
		// The backend result does not use static knowledge.
		out.KnowledgeSources = []KnowledgeSource{}
	}
	out.DurationMS = time.Since(started).Milliseconds()
	return out, nil
}
