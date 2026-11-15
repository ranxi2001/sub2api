package qqbot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

func replyMode(a AutonomousConfig) string {
	if a.ReplyMode == "" {
		return "natural"
	}
	return a.ReplyMode
}

func endsAutonomousConversation(text string) bool {
	text = strings.Trim(strings.ToLower(strings.TrimSpace(text)), "。，！!,. ")
	switch text {
	case "不用回复", "不用回了", "别回复了", "别回了", "不聊了", "结束聊天", "再见", "拜拜", "晚安", "bye", "goodbye":
		return true
	}
	return false
}

func autonomousPolicyText(a AutonomousConfig, e Event, commands ...CustomCommand) (string, string) {
	text, reason := autonomousText(e, commands...)
	if endsAutonomousConversation(autonomousContentText(e)) && (reason == "" || reason == "low_information") {
		return "", "conversation_ended"
	}
	if reason == "low_information" && (replyMode(a) == "always" || replyMode(a) == "active") {
		text = autonomousContentText(e)
		for _, r := range text {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSymbol(r) {
				return text, ""
			}
		}
	}
	return text, reason
}

func autonomousPolicyInstructions(a AutonomousConfig, followup bool) string {
	policy := "自然参与：有帮助或能自然接住话题时回复；对方结束话题、重复话题、已有人回答或无需插话时可沉默。"
	switch replyMode(a) {
	case "quiet":
		policy = "安静参与：只在明确求助或能补充必要信息时回复，其余可沉默。"
	case "active":
		policy = "积极参与：优先接住日常分享、短句、情绪和图片；不因内容简单就沉默，仅在明确结束、与别人对话或重复内容时跳过。"
	case "always":
		policy = "尽量回复：本条消息已通过本地筛选，请直接给出符合已设置人设和语气的回应。短句、笑声、感谢和情绪表达也应接住；信息不足时说明不确定或澄清，不能因没有新内容而沉默。"
	}
	protocol := `只输出一个 JSON 对象：{"action":"reply","text":"回复正文"} 或 {"action":"skip","reason":"原因枚举"}。跳过原因只能是 conversation_ended、addressed_elsewhere、repeated_topic、already_answered、insufficient_context、no_value；不输出思考过程、解释或代码围栏。`
	if replyMode(a) == "always" {
		protocol = "直接输出自然语言回复正文，不输出 JSON、action、skip、思考过程或代码围栏，不得选择沉默。"
	}
	if followup {
		policy += "本轮为同一成员的续聊，承接上一句，不当成全新问题。"
	}
	return autonomousInstructions + policy + protocol + fmt.Sprintf("回复不超过 %d 字。偏好话题仅供参考：%s。", a.MaxChars, strings.Join(a.Topics, "、"))
}

var autonomousSkipReasons = map[string]bool{
	"conversation_ended": true, "addressed_elsewhere": true, "repeated_topic": true,
	"already_answered": true, "insufficient_context": true, "no_value": true,
}

func parseAutonomousDecision(raw string, maxChars int, mode string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	var out struct {
		Action string `json:"action"`
		Text   string `json:"text"`
		Reason string `json:"reason"`
	}
	if mode == "always" && !strings.HasPrefix(raw, "{") {
		out.Action, out.Text = "reply", raw
	} else {
		d := json.NewDecoder(strings.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&out); err != nil {
			return "", "", errors.New("自主接话模型格式无效")
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return "", "", errors.New("自主接话模型格式无效")
		}
	}
	if out.Action == "skip" {
		if mode == "always" || out.Text != "" || (out.Reason != "" && !autonomousSkipReasons[out.Reason]) {
			return "", "", errors.New("自主接话模型违反回复策略")
		}
		if out.Reason == "" {
			return "", "model_skip", nil
		} // Older compatible models.
		return "", "model_skip_" + out.Reason, nil
	}
	out.Text = strings.TrimSpace(normalizeChatReply(out.Text))
	if out.Action != "reply" || out.Reason != "" || out.Text == "" || len([]rune(out.Text)) > maxChars || strings.ContainsAny(out.Text, "<>@") || strings.Contains(out.Text, "http://") || strings.Contains(out.Text, "https://") || strings.HasPrefix(out.Text, "```") {
		return "", "", errors.New("自主接话回复未通过格式或长度检查")
	}
	return out.Text, "", nil
}
