package qqbot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"
)

var ErrChannelStatusDisabled = errors.New("channel status disabled")
var ErrChannelStatusUnsupported = errors.New("channel status requires passive monitoring")

// ChannelStatusProvider returns only the fields safe to publish in a group chat.
// The host adapter owns monitoring configuration, aggregation and visibility.
type ChannelStatusProvider interface {
	ChannelStatus(context.Context) (*ChannelStatusReport, error)
}

type ChannelStatusMetric struct {
	OutputTPS           *float64
	HasRequests         bool
	InsufficientSamples bool
	ErrorRate           float64
	CacheRate           *float64
	TTFTP50Ms           *int64
	RPM                 float64
	TokensPerSecond     float64
}

type ChannelStatusGroup struct {
	Name    string
	Metrics ChannelStatusMetric
}

type ChannelStatusReport struct {
	UpdatedAt       time.Time
	PartialCoverage bool
	Backfilling     bool
	ShowThroughput  bool
	Overall         ChannelStatusMetric
	Groups          []ChannelStatusGroup
}

// SetChannelStatusProvider must be called before Start.
func (s *Service) SetChannelStatusProvider(provider ChannelStatusProvider) {
	s.channelStatus = provider
}

func (s *Service) sendChannelStatus(ctx context.Context, c Config, j Job, arg string) error {
	page := 1
	if arg != "" {
		var err error
		page, err = strconv.Atoi(arg)
		if err != nil || page < 1 {
			return s.send(ctx, c, j, "用法：渠道状态 [页码]", nil, 1)
		}
	}
	if s.channelStatus == nil {
		return s.send(ctx, c, j, "渠道状态暂时不可用，请稍后重试。", nil, 1)
	}
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	report, err := s.channelStatus.ChannelStatus(queryCtx)
	cancel()
	if err != nil || report == nil {
		text := "渠道状态查询失败，请稍后重试。"
		switch {
		case errors.Is(err, ErrChannelStatusDisabled):
			text = "渠道监控未开启。"
		case errors.Is(err, ErrChannelStatusUnsupported):
			text = "被动监控未开启。"
		}
		return s.send(ctx, c, j, text, nil, 1)
	}
	chunks := formatChannelStatus(report)
	// The existing passive reply path allows five messages for one source message.
	// Keep every group intact and make overflow accessible through explicit pages.
	const perPage = 5
	pages := (len(chunks) + perPage - 1) / perPage
	if page > pages {
		return s.send(ctx, c, j, fmt.Sprintf("共 %d 页\n翻页：渠道状态 [页码]", pages), nil, 1)
	}
	start, end := (page-1)*perPage, min(page*perPage, len(chunks))
	for i := start; i < end; i++ {
		text := chunks[i]
		if pages > 1 && i == end-1 {
			text += fmt.Sprintf("\n\n第 %d/%d 页", page, pages)
			if page < pages {
				text += fmt.Sprintf("\n下一页：渠道状态 %d", page+1)
			}
		}
		if err := s.send(ctx, c, j, text, nil, i-start+1); err != nil {
			return err
		}
	}
	return nil
}

func formatChannelStatus(report *ChannelStatusReport) []string {
	updated := "暂无数据"
	if !report.UpdatedAt.IsZero() {
		updated = report.UpdatedAt.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02 15:04") + "（北京时间）"
	}
	header := "📊 **渠道状态**\n近 90 分钟\n更新：" + updated
	if report.Backfilling {
		header += "\n正在补齐数据"
	} else if report.PartialCoverage {
		header += "\n仅覆盖部分时段"
	}
	text := header + "\n\n**整体**\n" + formatChannelStatusMetric(report.Overall, report.ShowThroughput)
	if len(report.Groups) == 0 {
		return []string{text + "\n\n分组：暂无可展示项"}
	}
	var chunks []string
	for i, group := range report.Groups {
		// Names stay on one line and cannot introduce Markdown markup.
		block := fmt.Sprintf("**%d. %s**\n%s", i+1, replyPlainLabel(group.Name), formatChannelStatusMetric(group.Metrics, report.ShowThroughput))
		if utf8.RuneCountInString(text)+utf8.RuneCountInString(block)+2 > 1400 {
			chunks = append(chunks, text)
			text = header + "\n分组（续）"
		}
		text += "\n\n" + block
	}
	return append(chunks, text)
}

func formatChannelStatusMetric(metric ChannelStatusMetric, throughput bool) string {
	if !metric.HasRequests {
		return "暂无请求数据"
	}
	// Do not present sparse traffic as a definitive outage (or perfect health).
	var text string
	if !metric.InsufficientSamples {
		// Match the web dashboard's success KPI, including ignored error categories.
		text = fmt.Sprintf("成功率：%.2f%%\n错误率：%.2f%%", (1-metric.ErrorRate)*100, metric.ErrorRate*100)
	} else {
		text = "成功率样本不足\n错误率样本不足"
	}
	if metric.CacheRate == nil {
		text += "\n缓存率：-"
	} else {
		text += fmt.Sprintf("\n缓存率：%.2f%%", *metric.CacheRate*100)
	}
	if metric.TTFTP50Ms == nil {
		text += "\n首 Token 暂无数据"
	} else {
		text += fmt.Sprintf("\n首 Token P50：%.2f 秒", float64(*metric.TTFTP50Ms)/1000)
	}
	if metric.OutputTPS == nil {
		text += "\n输出 TPS：暂无数据"
	} else {
		text += fmt.Sprintf("\n输出 TPS：%.2f token/s", *metric.OutputTPS)
	}
	if throughput {
		text += fmt.Sprintf("\nRPM：%.2f\n总吞吐：%.2f token/s", metric.RPM, metric.TokensPerSecond)
	}
	return text
}
