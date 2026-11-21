package service

import (
	"fmt"
	"html"
	"strings"
	"time"
)

type OpsAlertPlatformStats struct {
	Platform                   string
	Succeeded, Failed, Unknown int64
}

type opsAlertPlatformResult struct {
	rows      []OpsAlertPlatformStats
	available bool
}

func (r opsAlertPlatformResult) summary(rule *OpsAlertRule, filter OpsDashboardFilter, locale string) (headline, body string) {
	tr := func(en, zh string) string {
		if locale == notificationEmailLocaleChinese {
			return zh
		}
		return en
	}
	window := fmt.Sprintf("[%s, %s) UTC", filter.StartTime.UTC().Format(time.DateTime), filter.EndTime.UTC().Format(time.DateTime))
	if !r.available {
		return "", "<h3>" + tr("Platform statistics unavailable", "平台统计不可用") + "</h3><p>" + window + "</p>"
	}
	if len(r.rows) == 0 {
		return "", "<h3>" + tr("Platform health: no samples", "平台健康状态：无样本") + "</h3><p>" + window + "</p>"
	}
	// A display threshold only. Rule evaluation and notification cadence do not use it.
	const minSamples = 5
	var alerts []string
	var alertsHTML, otherHTML strings.Builder
	var total OpsAlertPlatformStats
	for _, row := range r.rows {
		n := row.Succeeded + row.Failed
		label := ChannelMonitorV2PlatformLabel(row.Platform)
		switch row.Platform {
		case PlatformOpenAI:
			label = "GPT/OpenAI"
		case PlatformAnthropic:
			label = "Claude/Anthropic"
		case "":
			label = tr("Unknown platform", "未知平台")
		}
		status := tr("Within threshold", "正常")
		rate := "—"
		if n > 0 {
			rate = fmt.Sprintf("%.2f%%", 100*float64(row.Failed)/float64(n))
		}
		abnormal := false
		switch {
		case n == 0 && row.Unknown == 0:
			status = tr("No samples", "无样本")
		case n == 0:
			status = tr("Unknown outcome", "终态未知")
		case n < minSamples:
			status = tr("Insufficient samples", "样本不足")
		default:
			numerator := row.Failed
			if rule.MetricType == "success_rate" {
				numerator = row.Succeeded
			}
			if compareMetric(100*float64(numerator)/float64(n), rule.Operator, rule.Threshold) {
				status = tr("Abnormal", "异常")
				alerts = append(alerts, label)
				abnormal = true
			} else if row.Unknown > 0 {
				status = tr("Known outcomes within threshold", "已知样本未达阈值")
			}
		}
		dest := &otherHTML
		color := "#475569"
		if abnormal {
			dest = &alertsHTML
			color = "#b42318"
		}
		fmt.Fprintf(dest, `<tr><th scope="row" align="left">%s</th><td style="color:%s"><strong>%s</strong></td><td>%d</td><td>%d</td><td>%s</td><td>%d</td></tr>`,
			html.EscapeString(label), color, status, n, row.Failed, rate, row.Unknown)
		total.Succeeded += row.Succeeded
		total.Failed += row.Failed
		total.Unknown += row.Unknown
	}
	title := tr("Platform health", "平台健康状态")
	if len(alerts) > 0 {
		headline = tr("Abnormal: ", "异常平台：") + strings.Join(alerts, ", ")
		title = headline
	}
	var out strings.Builder
	fmt.Fprintf(&out, `<h3 style="margin:8px 0">%s</h3><p style="font-size:13px">%s</p><table cellspacing="0" cellpadding="5" style="width:100%%;border-collapse:collapse;font-size:13px;text-align:left"><thead><tr style="background:#eef2f6">`, html.EscapeString(title), window)
	for _, header := range []string{tr("Platform", "平台"), tr("Status", "状态"), tr("Counted", "请求"), tr("Failed", "失败"), tr("Error rate", "错误率"), tr("Unknown", "终态未知")} {
		fmt.Fprintf(&out, `<th scope="col">%s</th>`, header)
	}
	_, _ = out.WriteString("</tr></thead><tbody>")
	_, _ = out.WriteString(alertsHTML.String())
	_, _ = out.WriteString(otherHTML.String())
	_, _ = out.WriteString("</tbody></table>")
	fmt.Fprintf(&out, "<p>"+tr("Total: %d failed / %d known requests, %d unknown.", "合计：失败 %d / 已知终态 %d，另有 %d 个终态未知。")+"</p>", total.Failed, total.Succeeded+total.Failed, total.Unknown)
	fmt.Fprintf(&out, "<p>"+tr("Threshold: %s %s %g%% with at least %d known requests per platform.", "判定条件：%s %s %g%%，每个平台至少 %d 个已知终态请求。")+"</p>", html.EscapeString(rule.MetricType), html.EscapeString(rule.Operator), rule.Threshold, minSamples)
	_, _ = out.WriteString("<p>" + tr("Counted = successes + final failures, including in-stream failures. Unknown outcomes, business limits and client cancellations are excluded. Attribution uses the recorded usage platform, then the selected account or logged routing platform. The trigger uses the existing SLA overview.", "计入请求为成功和最终失败之和，含流内失败。终态未知、业务限制及客户端取消不计入分母。跨协议按用量记录的平台归属，缺失时依次取所选账号平台和错误日志的路由平台。触发值沿用原 SLA 总览。") + "</p>")
	return headline, out.String()
}

// Preview uses the same renderer as delivery and never reads monitoring data.
func previewOpsAlertPlatforms(locale string) (string, string) {
	end := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	r := opsAlertPlatformResult{available: true, rows: []OpsAlertPlatformStats{
		{Platform: PlatformOpenAI, Succeeded: 46},
		{Platform: PlatformAnthropic, Succeeded: 29},
		{Platform: PlatformDeepseek, Succeeded: 9, Failed: 12},
	}}
	return r.summary(&OpsAlertRule{MetricType: "error_rate", Operator: ">=", Threshold: 10}, OpsDashboardFilter{StartTime: end.Add(-10 * time.Minute), EndTime: end}, locale)
}
