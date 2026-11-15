package qqbot

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

type spamReportReview struct {
	Decision   string   `json:"decision"`
	Category   string   `json:"category"`
	Confidence float64  `json:"confidence"`
	Evidence   []string `json:"evidence"`
	Reason     string   `json:"reason"`
	Rejection  string   `json:"-"`
}

// A second reviewer cannot turn an uncertain first review into a punishment.
// Both reviews must independently find direct, category-specific source text.
func parseSpamReportReview(raw, source string) (spamReportReview, error) {
	var review spamReportReview
	if len(raw) > 8192 || !utf8.ValidString(raw) {
		return review, errors.New("invalid spam review size")
	}
	if !spamReportStrictObject(raw) {
		return review, errors.New("invalid spam review object")
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&review); err != nil {
		return review, errors.New("invalid spam review JSON")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return review, errors.New("extra spam review data")
	}
	if review.Confidence < 0 || review.Confidence > 1 || math.IsNaN(review.Confidence) || math.IsInf(review.Confidence, 0) ||
		strings.TrimSpace(review.Reason) == "" || utf8.RuneCountInString(review.Reason) > 240 || strings.ContainsFunc(review.Reason, unicode.IsControl) {
		return review, errors.New("invalid spam review fields")
	}
	if review.Decision == "uncertain" {
		if review.Category != "" || review.Evidence == nil || len(review.Evidence) != 0 {
			return review, errors.New("invalid uncertain spam review")
		}
		return review, nil
	}
	if review.Decision != "spam" || (review.Category != "advertising" && review.Category != "phishing" && review.Category != "scam") ||
		len(review.Evidence) == 0 || len(review.Evidence) > 3 {
		return review, errors.New("invalid spam review decision")
	}
	// Confidence is necessary, never sufficient. A deterministic, intentionally
	// narrow source/evidence check protects ordinary links and injected verdicts.
	if review.Confidence < .99 {
		review.Decision = "uncertain"
		review.Rejection = "AI 审核置信度未达到 0.99 的自动处理门槛。"
		return review, nil
	}
	if !spamReportUnambiguousSource(source) {
		review.Decision = "uncertain"
		review.Rejection = "原文存在歧义或不符合自动审核的文本条件。"
		return review, nil
	}
	for _, evidence := range review.Evidence {
		if evidence != strings.TrimSpace(evidence) || utf8.RuneCountInString(evidence) < 12 || utf8.RuneCountInString(evidence) > 1000 ||
			!strings.Contains(source, evidence) || !spamReportCategoryEvidence(review.Category, evidence) {
			review.Decision = "uncertain"
			review.Rejection = "AI 提供的违规证据未通过原文逐字匹配及完整行为证据校验。"
			return review, nil
		}
	}
	return review, nil
}

// encoding/json otherwise accepts duplicate keys and null scalar values. Both
// make an enforcement decision ambiguous, so reject them before typed decoding.
func spamReportStrictObject(raw string) bool {
	d := json.NewDecoder(strings.NewReader(raw))
	opening, err := d.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool, 5)
	for d.More() {
		keyToken, err := d.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || seen[key] {
			return false
		}
		switch key {
		case "decision", "category", "confidence", "evidence", "reason":
		default:
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil || strings.TrimSpace(string(value)) == "null" {
			return false
		}
	}
	closing, err := d.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != 5 {
		return false
	}
	var extra any
	return d.Decode(&extra) == io.EOF
}

func spamReportContains(text string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func spamReportUnambiguousSource(source string) bool {
	if strings.TrimSpace(source) == "" || !utf8.ValidString(source) || len(source) > 16384 || utf8.RuneCountInString(source) > 4096 {
		return false
	}
	source = strings.ToLower(source)
	// A narrow automatic enforcement path is preferable to silencing someone
	// who is asking about or warning others about the very same spam language.
	return !spamReportContains(source,
		"?", "？", "\"", "“", "”", "‘", "’", "「", "」", "『", "』", "`", "<", ">", "{", "}",
		"不要", "别信", "别点", "勿", "警惕", "谨防", "注意", "诈骗", "钓鱼", "骗子", "反诈", "举报", "提醒", "警告", "新闻", "报道", "转发", "引用", "摘录", "有人说", "他发", "她发", "他们说", "比如", "例如", "假设", "举例", "测试", "玩笑", "段子", "是否", "怎么", "如何", "为什么", "是真的吗", "不建议", "不推荐", "切勿", "不能", "拒绝", "不会", "无需", "不需要", "不提供", "不出售", "不要相信", "未经验证", "广告", "垃圾信息", "吗", "么", "呢", "教程", "讨论", "辨别", "求购", "我想买",
		"忽略", "系统提示", "提示词", "审核器", "审核结果", "判定为", "判为", "输出json", "输出 json", "confidence", "evidence", "developer", "system prompt", "ignore previous", "ignore all", "follow these", "must output", "classify this", "decision", "jailbreak",
		"do not", "don't", "warning", "beware", "scam", "phishing", "for example", "example:", "quoted", "quote:", "someone said", "hypothetical", "joking", "is this", "how to", "why ", "looking for")
}

func spamReportCategoryEvidence(category, evidence string) bool {
	text := strings.ToLower(evidence)
	switch category {
	case "advertising":
		commercial := spamReportContains(text, "出售", "代购", "代充", "代开", "低价", "特价", "优惠价", "折扣", "返佣", "招商", "招代理", "包月", "买一送", "全网最低", "限时优惠", "sale", "discount", "buy one", "reseller")
		solicitation := spamReportContains(text, "加微信", "加我微信", "加我vx", "加vx", "联系我", "私聊我", "扫码购买", "点击购买", "立即下单", "下单购买", "进群购买", "私信购买", "私信下单", "联系客服购买", "buy now", "order now", "contact me", "dm me")
		return commercial && solicitation
	case "phishing":
		link := spamReportContains(text, "https://", "http://")
		// Asking someone to log in on a URL is not proof of phishing. Only an
		// explicit request to disclose credentials to another party qualifies.
		credentials := spamReportContains(text, "发我密码", "把密码发给", "密码发给客服", "密码发送给", "发我验证码", "把验证码发给", "验证码发给客服", "验证码发送给", "send me your password", "send us your password", "send me your verification code", "send us your verification code")
		pressure := spamReportContains(text, "账号异常", "账户异常", "账号冻结", "账户冻结", "立即验证", "否则封号", "解除冻结", "安全中心", "官方客服", "account suspended", "verify immediately", "security center")
		return link && credentials && pressure
	case "scam":
		payment := spamReportContains(text, "先转账", "立即转账", "先付款", "先交保证金", "缴纳保证金", "支付保证金", "先交押金", "先充值", "转账到", "send money", "pay a deposit", "transfer money")
		inducement := spamReportContains(text, "刷单", "稳赚", "保本高收益", "保证收益", "日赚", "日收益", "返利", "本金翻倍", "解冻资金", "领取奖金", "领取中奖", "guaranteed profit", "double your money", "claim your prize")
		return payment && inducement
	}
	return false
}

func spamReportAddTokens(total, used int64) int64 {
	if used <= 0 {
		return total
	}
	if total > math.MaxInt64-used {
		return math.MaxInt64
	}
	return total + used
}
