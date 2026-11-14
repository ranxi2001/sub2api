package qqbot

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxCustomCommands    = 20
	maxCustomReplyLength = 2000
)

type CustomCommand struct {
	Command string `json:"command"`
	Reply   string `json:"reply"`
	Enabled bool   `json:"enabled"`
}

var customCommandPattern = regexp.MustCompile(`^/[\p{L}\p{N}_-]{1,32}$`)

func isBuiltinCommand(cmd string) bool {
	if cmd == "我的优惠券" {
		return true
	}
	switch cmd {
	case "帮助", "签到", "今日用量", "成长任务", "邀请奖励", "邀请活动", "我的邀请", "我的余额", "余额", "我的奖励", "抽奖", "抽奖次数", "我的抽奖", "抽奖活动", "渠道状态", "生图", "价格", "绑定", "转账", "今日排行榜", "今日排行版":
		return true
	}
	return false
}

// Removed built-ins cannot be revived accidentally by stored custom replies.
func isRetiredCommand(cmd string) bool {
	switch cmd {
	case "help", "我的", "福利", "领取任务", "画图", "奖励兑换", "举报垃圾信息":
		return true
	}
	return false
}

func normalizeCustomCommands(commands []CustomCommand) []CustomCommand {
	normalized := make([]CustomCommand, len(commands))
	for i, cmd := range commands {
		cmd.Command = strings.TrimSpace(cmd.Command)
		if cmd.Command != "" && !strings.HasPrefix(cmd.Command, "/") {
			cmd.Command = "/" + cmd.Command
		}
		cmd.Reply = strings.TrimSpace(cmd.Reply)
		normalized[i] = cmd
	}
	return normalized
}

func validateCustomCommands(commands []CustomCommand) error {
	if len(commands) > maxCustomCommands {
		return fmt.Errorf("自定义指令最多 %d 条", maxCustomCommands)
	}
	seen := make(map[string]bool, len(commands))
	for i, cmd := range commands {
		if !customCommandPattern.MatchString(cmd.Command) {
			return fmt.Errorf("第 %d 条自定义指令名称须为 1～32 个中文、字母、数字、下划线或短横线", i+1)
		}
		if isBuiltinCommand(strings.TrimPrefix(cmd.Command, "/")) || isRetiredCommand(strings.TrimPrefix(cmd.Command, "/")) {
			return fmt.Errorf("自定义指令 %s 与内置指令重名", cmd.Command)
		}
		if seen[cmd.Command] {
			return fmt.Errorf("自定义指令 %s 重复", cmd.Command)
		}
		seen[cmd.Command] = true
		if strings.TrimSpace(cmd.Reply) == "" || utf8.RuneCountInString(cmd.Reply) > maxCustomReplyLength {
			return errors.New("自定义指令的回复内容须为 1～2000 字")
		}
	}
	return nil
}

func customCommandReply(commands []CustomCommand, cmd, arg string) (string, bool) {
	if cmd == "" || arg != "" || isBuiltinCommand(cmd) || isRetiredCommand(cmd) {
		return "", false
	}
	for _, custom := range commands {
		if custom.Enabled && custom.Command == "/"+cmd {
			return custom.Reply, true
		}
	}
	return "", false
}
