package qqbot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

func paginateCommand(body string) []string {
	var pages []string
	current := ""
	for _, paragraph := range strings.Split(body, "\n\n") {
		if strings.TrimSpace(paragraph) == "" {
			continue
		}
		// Operator-defined rules can be long. Split on lines first, without
		// applying the truncation used for unbounded model-generated replies.
		for len([]rune(paragraph)) > 1300 {
			runes := []rune(paragraph)
			cut := 1300
			for i := cut - 1; i >= 650; i-- {
				if runes[i] == '\n' {
					cut = i + 1
					break
				}
			}
			if current != "" {
				pages = append(pages, current)
				current = ""
			}
			pages = append(pages, string(runes[:cut]))
			paragraph = string(runes[cut:])
		}
		if current != "" && (len([]rune(current))+len([]rune(paragraph))+2 > 1300) {
			pages = append(pages, current)
			current = ""
		}
		if current != "" {
			current += "\n\n"
		}
		current += paragraph
	}
	if current != "" {
		pages = append(pages, current)
	}
	return pages
}

func (s *Service) sendCommandPage(ctx context.Context, c Config, j Job, cmd, body, arg string) error {
	page, ok := commandPageNumber(arg)
	if !ok {
		return s.send(ctx, c, j, "用法："+cmd+"；翻页："+cmd+" 页码。", nil, 1)
	}
	pages := paginateCommand(body)
	if len(pages) == 0 {
		return s.send(ctx, c, j, "暂无内容。", nil, 1)
	}
	if page > len(pages) {
		return s.send(ctx, c, j, fmt.Sprintf("共 %d 页，请发送 %s 页码。", len(pages), cmd), nil, 1)
	}
	text := pages[page-1]
	var keyboard *messageKeyboard
	if len(pages) > 1 {
		row := keyboardRow{}
		for _, dest := range []int{page - 1, page + 1} {
			if dest < 1 || dest > len(pages) {
				continue
			}
			label := "下一页"
			if dest < page {
				label = "上一页"
			}
			command := fmt.Sprintf("%s %d", cmd, dest)
			row.Buttons = append(row.Buttons, helpCommandButton(label, command, command))
		}
		keyboard = &messageKeyboard{Content: keyboardContent{Rows: []keyboardRow{row}}}
		text += fmt.Sprintf("\n\n第 %d/%d 页\n翻页：%s 页码", page, len(pages), cmd)
	}
	return s.sendFormattedWithKeyboard(ctx, c, j, normalizeReplyLineBreaks(text), nil, 1, keyboard)
}

func commandPageNumber(arg string) (int, bool) {
	if arg == "" {
		return 1, true
	}
	page, err := strconv.Atoi(arg)
	return page, err == nil && page > 0 && strconv.Itoa(page) == arg
}
