package service

// gpt62DefaultPricing uses the official Standard short-context rates checked on
// 2026-11-18: https://developers.openai.com/api/docs/pricing?context-length=short
// The owner confirms Pro uses the same rates as Astra. Exact catalog and
// channel prices take precedence; tier/context policies are separate.
func gpt62DefaultPricing(model string) *LiteLLMModelPricing {
	var input, output, write, read float64
	switch model {
	case "gpt-6.2-astra", "gpt-6.2-astra-pro":
		input, output, write, read = 8e-6, 40e-6, 10e-6, 0.4e-6
	case "gpt-6.2-sol":
		input, output, write, read = 2e-6, 10e-6, 2.5e-6, 0.1e-6
	default:
		return nil
	}
	write12h := write * 2
	return &LiteLLMModelPricing{
		InputCostPerToken:                   input,
		OutputCostPerToken:                  output,
		CacheCreationInputTokenCost:         write,
		CacheCreationInputTokenCost12h:      &write12h,
		CacheCreationInputTokenCostExplicit: true,
		CacheReadInputTokenCost:             read,
		LiteLLMProvider:                     "openai",
		Mode:                                "responses",
		SupportsPromptCaching:               true,
	}
}
