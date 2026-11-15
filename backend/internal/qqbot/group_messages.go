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

const (
	groupAtMessageCreate = "GROUP_AT_MESSAGE_CREATE"
	groupMessageCreate   = "GROUP_MESSAGE_CREATE"
)

func isGroupMessageEvent(eventType string) bool {
	return eventType == groupAtMessageCreate || eventType == groupMessageCreate
}

func unmentionedGroupCommand(e Event, customCommands ...CustomCommand) bool {
	// Commands addressed to someone else are not unmentioned commands.
	if len(groupMentionIDs(e)) > 0 {
		return false
	}
	cmd, arg := command(e.Content, customCommands...)
	if isBuiltinCommand(cmd) {
		return true
	}
	_, ok := customCommandReply(customCommands, cmd, arg)
	return ok
}

// isBuiltinOrCustomCommand reports whether the event content, after stripping
// any leading mention, is a command the bot knows about. Used to route
// "@bot cmd" messages to the command handler rather than the AI chat path.
func isBuiltinOrCustomCommand(e Event, customCommands []CustomCommand) bool {
	cmd, arg := command(e.Content, customCommands...)
	if cmd == "" {
		return false
	}
	if isBuiltinCommand(cmd) {
		return true
	}
	_, ok := customCommandReply(customCommands, cmd, arg)
	return ok
}

func groupMentionIDs(e Event) []string {
	ids := make([]string, 0, len(e.Mentions)*2+1)
	for _, mention := range e.Mentions {
		if mention.ID != "" {
			ids = append(ids, mention.ID)
		}
		if mention.MemberID != "" {
			ids = append(ids, mention.MemberID)
		}
	}
	if prefix := mentionPrefix.FindString(strings.TrimSpace(e.Content)); prefix != "" {
		id := strings.TrimSuffix(strings.TrimSpace(prefix), ">")
		ids = append(ids, strings.TrimPrefix(strings.TrimPrefix(id, "<@"), "!"))
	}
	return ids
}

func shouldQueueGroupMessage(eventType string, e Event, customCommands ...CustomCommand) bool {
	if e.Author.Bot {
		return false
	}
	if eventType == groupAtMessageCreate {
		return true
	}
	return eventType == groupMessageCreate && (unmentionedGroupCommand(e, customCommands...) || len(groupMentionIDs(e)) > 0 || e.referenceID() != "")
}

type groupBotIdentity struct {
	memberID string
	expires  time.Time
	err      error
}

func (s *Service) groupMessageMentionsBot(ctx context.Context, c Config, e Event) (bool, error) {
	ids := groupMentionIDs(e)
	if len(ids) == 0 {
		return false, nil
	}
	memberID, err := s.groupBotMemberID(ctx, c, e.GroupID)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id == memberID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) groupBotMemberID(ctx context.Context, c Config, group string) (string, error) {
	s.groupBotMu.Lock()
	defer s.groupBotMu.Unlock()
	key := codeHash(c.AppID + ":" + c.AppSecret + ":" + s.qqURL + ":" + group)
	if c.Sandbox {
		key += ":sandbox"
	}
	if cached, ok := s.groupBots[key]; ok && time.Now().Before(cached.expires) {
		return cached.memberID, cached.err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := s.qqRequestMethod(ctx, c, http.MethodGet, "/v2/groups/"+url.PathEscape(group)+"/bot_state", nil)
	var state struct {
		MemberID string `json:"member_openid"`
	}
	if err == nil && (json.Unmarshal(raw, &state) != nil || state.MemberID == "") {
		err = errors.New("QQ 群机器人身份响应无效")
	}
	ttl := 10 * time.Minute
	if err != nil {
		// Never treat an arbitrary mentioned bot as this bot when identity lookup fails.
		ttl = time.Minute
		err = errors.New("无法确认群内机器人身份，请检查 QQ bot_state 接口权限")
	}
	if s.groupBots == nil {
		s.groupBots = make(map[string]groupBotIdentity)
	}
	for cachedKey, cached := range s.groupBots {
		if time.Now().After(cached.expires) {
			delete(s.groupBots, cachedKey)
		}
	}
	s.groupBots[key] = groupBotIdentity{memberID: state.MemberID, expires: time.Now().Add(ttl), err: err}
	return state.MemberID, err
}
