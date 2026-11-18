package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// openAISubscriptionLongContextExempt 按实际凭据和最终发送模型判断订阅豁免。
// GPT-6.2 Sol / Astra、GPT-6.1 Sol / Terra 的订阅请求豁免模型附加费；API Key 仍使用 API 价卡。
// 使用发送模型，避免客户别名、渠道计费模型或响应模型计费切换改变上游订阅规则。
func openAISubscriptionLongContextExempt(account *Account, result *OpenAIForwardResult) bool {
	if !account.IsOpenAIOAuthLike() || result == nil {
		return false
	}
	model := strings.TrimSpace(result.UpstreamModel)
	if model == "" {
		model = result.Model
	}
	return openai.GPT62ModelID(model) != "" || openai.IsGPT61SolModelSpelling(model) || openai.IsGPT61TerraModelSpelling(model)
}
