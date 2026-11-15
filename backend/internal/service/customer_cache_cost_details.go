package service

// CustomerCacheCostDetail records the actual priced, disjoint token bucket.
// UnitPrice is per token before the customer multiplier, including resolved
// context/time/reasoning adjustments. It is display evidence only.
type CustomerCacheCostDetail struct {
	Kind      string  `json:"kind"`
	Tokens    int     `json:"tokens"`
	UnitPrice float64 `json:"unit_price"`
}

// Called with the same selected prices and normalized buckets used by
// computeTokenBreakdown. Never resolves a second catalogue/group quote.
func resolvedCustomerCacheCosts(pricing *ModelPricing, tokens UsageTokens, writePrice, writeMultiplier, readPrice, tierMultiplier float64) []CustomerCacheCostDetail {
	lines := make([]CustomerCacheCostDetail, 0, 4)
	add := func(kind string, count int, price float64) {
		if count > 0 {
			lines = append(lines, CustomerCacheCostDetail{Kind: kind, Tokens: count, UnitPrice: price * tierMultiplier})
		}
	}
	if _, ok := openAICacheCreationTTLCost(tokens, writePrice, pricing.CacheCreation12hPricePerToken); ok {
		longPrice := writePrice
		if pricing.CacheCreation12hPricePerToken != nil {
			longPrice = *pricing.CacheCreation12hPricePerToken
		}
		add("write_30m", tokens.CacheCreation30mTokens, writePrice*writeMultiplier)
		add("write_12h", tokens.CacheCreation12hTokens, longPrice*writeMultiplier)
	} else if pricing.SupportsCacheBreakdown && (pricing.CacheCreation5mPrice > 0 || pricing.CacheCreation1hPrice > 0) {
		short, long := normalizeCacheCreationBreakdown(tokens)
		if short == 0 && long == 0 && tokens.CacheCreationTokens > 0 {
			short = tokens.CacheCreationTokens
		}
		add("write_5m", short, pricing.CacheCreation5mPrice*writeMultiplier)
		add("write_1h", long, pricing.CacheCreation1hPrice*writeMultiplier)
	} else {
		add("write", tokens.CacheCreationTokens, writePrice*writeMultiplier)
	}
	imageRead := min(max(tokens.ImageCacheReadTokens, 0), max(tokens.CacheReadTokens, 0))
	if imageRead > 0 && pricing.ImageCacheReadPricePerToken > 0 {
		add("read", tokens.CacheReadTokens-imageRead, readPrice)
		add("read_image", imageRead, pricing.ImageCacheReadPricePerToken)
	} else {
		add("read", tokens.CacheReadTokens, readPrice)
	}
	return lines
}
