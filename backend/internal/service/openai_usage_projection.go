package service

// Keep usage projection independent of the quota-observation and forwarding
// implementations, including historical comparison fixtures.
func (usage OpenAIUsage) billingUsageTokens() UsageTokens {
	creationTokens := usage.CacheCreationInputTokens
	if creationTokens <= 0 {
		creationTokens = max(usage.CacheCreation5mTokens, 0) + max(usage.CacheCreation1hTokens, 0)
	}
	tokens := UsageTokens{
		InputTokens:            max(usage.InputTokens-usage.CacheReadInputTokens-creationTokens, 0),
		ImageInputTokens:       max(usage.ImageInputTokens-usage.ImageCacheReadTokens, 0),
		ImageCacheReadTokens:   usage.ImageCacheReadTokens,
		OutputTokens:           usage.OutputTokens,
		CacheCreationTokens:    creationTokens,
		CacheCreation5mTokens:  usage.CacheCreation5mTokens,
		CacheCreation1hTokens:  usage.CacheCreation1hTokens,
		CacheCreation30mTokens: usage.CacheCreation30mInputTokens,
		CacheCreation12hTokens: usage.CacheCreation12hInputTokens,
		CacheReadTokens:        usage.CacheReadInputTokens,
		ImageOutputTokens:      usage.ImageOutputTokens,
	}
	return tokens
}

func (s *BillingService) calculateCostWithServiceTierPolicy(
	model string,
	tokens UsageTokens,
	rateMultiplier float64,
	serviceTier string,
	longContextBillingEnabled bool,
) (*CostBreakdown, error) {
	return s.calculateCostInternalWithPolicy(model, tokens, rateMultiplier, serviceTier, nil, longContextBillingEnabled)
}
