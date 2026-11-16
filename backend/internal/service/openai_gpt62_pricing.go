package service

// gpt62DefaultPricing is the operator's 2026-11-16 rate card (USD/token).
// It is not a claim about an official OpenAI list price. Exact catalog and
// channel prices take precedence. Unspecified tier/context policies use the
// existing generic behavior, without importing an older model's surcharges.
func gpt62DefaultPricing(model string) *LiteLLMModelPricing {
	input, output, write, read := 10e-6, 50e-6, 12.5e-6, 1e-6
	switch model {
	case "gpt-6.2-astra", "gpt-6.2-astra-pro":
	case "gpt-6.2-sol":
		input, output, write, read = 2e-6, 10e-6, 2.5e-6, 0.1e-6
	default:
		return nil
	}
	return &LiteLLMModelPricing{
		InputCostPerToken:                   input,
		OutputCostPerToken:                  output,
		CacheCreationInputTokenCost:         write,
		CacheCreationInputTokenCost12h:      &write,
		CacheCreationInputTokenCostExplicit: true,
		CacheReadInputTokenCost:             read,
		LiteLLMProvider:                     "openai",
		Mode:                                "responses",
		SupportsPromptCaching:               true,
	}
}
