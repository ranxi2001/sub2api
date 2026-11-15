package qqbot

import (
	"regexp"
	"strings"
	"unicode"
)

var naturalAccountRequests = []struct {
	command string
	pattern *regexp.Regexp
}{
	{"我的余额", naturalAccountPattern(`(?:(?:账户|账号|钱包)的?)?(?:余额|剩余额度)(?:(?:还有|还剩|剩下|剩|有|是)?多少(?:钱|美元)?|剩多少)?`)},
	{"我的余额", naturalAccountPattern(`(?:(?:账户|账号|钱包)(?:里|中)?)?(?:还有|还剩|剩下|剩|有)多少(?:钱|美元)`)},
	{"今日用量", naturalAccountPattern(`(?:今天|今日)(?:的)?(?:用量|使用量|使用情况|消耗|消费|花费)(?:(?:有|是|了)?多少)?`)},
	{"抽奖次数", naturalAccountPattern(`(?:剩余|剩下|剩)?(?:的)?抽奖(?:次数|机会)(?:(?:还有|还剩|剩下|剩|有|是)?多少(?:次)?|剩多少(?:次)?)?`)},
	{"抽奖次数", naturalAccountPattern(`(?:还有|还剩|剩下|剩|有)多少(?:次)?抽奖(?:次数|机会)?`)},
}

func naturalAccountPattern(subject string) *regexp.Regexp {
	return regexp.MustCompile(`^(?:(?:请问|请|麻烦|劳驾)(?:你)?\s*)?(?:(?:帮|给|替)我\s*)?(?:(?:查询|查看|查一查|查一下|查查|查下|查|看一看|看一下|看看|看下|看|告诉我)\s*)?(?:(?:我的?|本人(?:的)?)\s*)?` + subject + `(?:呢|呀|啊|吧|吗|么)?$`)
}

// Match complete requests, never keywords embedded in instructions, quotations,
// questions about another member, or requests that also perform an operation.
// The caller must first verify that the current message addresses this bot.
func naturalAccountCommand(text string) string {
	text = strings.TrimSpace(mentionPrefix.ReplaceAllString(strings.TrimSpace(text), ""))
	text = strings.TrimRightFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("。.!！?？～~", r)
	})
	for _, request := range naturalAccountRequests {
		if request.pattern.MatchString(text) {
			return request.command
		}
	}
	return ""
}

// Apply the same privacy rule before retaining either group history or short
// operator-facing member hints, including command arguments on later lines.
func privateCommandText(text string, customCommands ...CustomCommand) bool {
	for _, line := range strings.Split(text, "\n") {
		if cmd, _ := command(line, customCommands...); cmd != "" || naturalAccountCommand(line) != "" || containsRewardCode(line) {
			return true
		}
	}
	return false
}
