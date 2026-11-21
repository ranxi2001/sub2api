//go:build unit

package service

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpsAlertPlatformTemplatePreview(t *testing.T) {
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	for _, locale := range []string{"zh", "en"} {
		// Official and saved pre-upgrade templates both use the settings preview.
		for _, custom := range []string{"", "<p>{{rule_name}}</p>"} {
			preview, err := svc.PreviewTemplate(t.Context(), NotificationEmailPreviewInput{Event: NotificationEmailEventOpsAlert, Locale: locale, HTML: custom})
			require.NoError(t, err)
			require.Contains(t, preview.Subject, "DeepSeek")
			require.NotContains(t, preview.Subject, "GPT")
			require.Contains(t, preview.HTML, `<th scope="row" align="left">DeepSeek</th>`)
			require.Contains(t, preview.HTML, "57.14%")
			require.Contains(t, preview.HTML, "GPT/OpenAI")
			require.Contains(t, preview.HTML, "Claude/Anthropic")
			require.Less(t, strings.Index(preview.HTML, "57.14%"), strings.Index(preview.HTML, "GPT/OpenAI"))
		}
	}
}

func TestOpsAlertPlatformUncertainResults(t *testing.T) {
	rule := &OpsAlertRule{MetricType: "success_rate", Operator: "<", Threshold: 95}
	window := OpsDashboardFilter{StartTime: time.Unix(0, 0), EndTime: time.Unix(60, 0)}
	for _, tc := range []struct {
		result opsAlertPlatformResult
		want   string
	}{
		{opsAlertPlatformResult{}, "统计不可用"},
		{opsAlertPlatformResult{available: true}, "无样本"},
		{opsAlertPlatformResult{available: true, rows: []OpsAlertPlatformStats{{Platform: PlatformOpenAI}}}, "无样本"},
		{opsAlertPlatformResult{available: true, rows: []OpsAlertPlatformStats{{Unknown: 9}}}, "未知平台"},
		{opsAlertPlatformResult{available: true, rows: []OpsAlertPlatformStats{{Failed: 4}}}, "样本不足"},
	} {
		head, body := tc.result.summary(rule, window, "zh")
		require.Empty(t, head)
		require.Contains(t, body, tc.want)
		require.NotContains(t, body, ">0.00%</td>")
	}
	result := opsAlertPlatformResult{available: true, rows: []OpsAlertPlatformStats{{Platform: "<script>", Failed: 5}}}
	head, body := result.summary(rule, window, "zh")
	require.Contains(t, head, "异常平台")
	require.NotContains(t, body, "<script>")
	require.Contains(t, body, "&lt;script&gt;")
}
