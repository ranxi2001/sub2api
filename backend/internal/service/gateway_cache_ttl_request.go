package service

import "github.com/tidwall/gjson"

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
