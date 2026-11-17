package service

import (
	"math"

	"github.com/tidwall/gjson"
)

// requestedCacheTTLPaths 覆盖 Anthropic Messages 请求里可以放置 cache_control 的位置。
var requestedCacheTTLPaths = []string{
	"cache_control.ttl",
	"system.#.cache_control.ttl",
	"tools.#.cache_control.ttl",
	"messages.#.content.#.cache_control.ttl",
}

// requestedAnthropicCacheTTL 返回客户端请求的缓存 TTL：任一 cache_control 显式请求 1h 即为 "1h"；
// 未设置 ttl、只请求 5m、没有断点或请求体无法解析时为 "5m"，与 Anthropic API 的默认值一致。
func requestedAnthropicCacheTTL(body []byte) string {
	if !gjson.ValidBytes(body) {
		return cacheTTLTarget5m
	}
	for _, path := range requestedCacheTTLPaths {
		if cacheTTLResultContains1h(gjson.GetBytes(body, path)) {
			return cacheTTLTarget1h
		}
	}
	return cacheTTLTarget5m
}

// reclassifyCacheCreationByRequestedTTL 把全部缓存写入归到客户端请求的 TTL 档位。
// 先按计费同样的规则规范化分桶（负数按 0，超出 aggregate 时按比例收敛），再以显式为正的
// aggregate 作为写入总量；aggregate 缺失时取非负分桶之和（饱和相加防溢出），并回写 aggregate。
// 结果保证 5m+1h == aggregate，账单数量、费用与保存的快照一致。
// 返回 true 表示有写入量从另一档位改归到 target（即 TTL 被替换）。
func reclassifyCacheCreationByRequestedTTL(usage *ClaudeUsage, target string) bool {
	if usage == nil {
		return false
	}
	before5m, before1h := normalizeCacheCreationBreakdown(UsageTokens{
		CacheCreationTokens:   usage.CacheCreationInputTokens,
		CacheCreation5mTokens: usage.CacheCreation5mTokens,
		CacheCreation1hTokens: usage.CacheCreation1hTokens,
	})
	total := usage.CacheCreationInputTokens
	if total <= 0 {
		total = before5m
		if before1h > math.MaxInt-total {
			total = math.MaxInt
		} else {
			total += before1h
		}
	}
	if total <= 0 {
		return false
	}
	// 与计费回退一致：只有 aggregate、没有明细时视为 5m。
	if before5m == 0 && before1h == 0 {
		before5m = total
	}

	usage.CacheCreationInputTokens = total
	if target == cacheTTLTarget1h {
		usage.CacheCreation5mTokens, usage.CacheCreation1hTokens = 0, total
		return before5m > 0
	}
	usage.CacheCreation5mTokens, usage.CacheCreation1hTokens = total, 0
	return before1h > 0
}

// cacheTTLResultContains1h 递归展开 gjson 的 # 查询结果（可能是嵌套数组）。
func cacheTTLResultContains1h(r gjson.Result) bool {
	if r.IsArray() {
		found := false
		r.ForEach(func(_, v gjson.Result) bool {
			found = cacheTTLResultContains1h(v)
			return !found
		})
		return found
	}
	return r.String() == cacheTTLTarget1h
}
