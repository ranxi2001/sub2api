package middleware

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	ippkg "github.com/Wei-Shaw/sub2api/internal/pkg/ip"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// RateLimitFailureMode Redis 故障策略
type RateLimitFailureMode int

const (
	RateLimitFailOpen RateLimitFailureMode = iota
	RateLimitFailClose
)

// RateLimitOptions 限流可选配置
type RateLimitOptions struct {
	FailureMode RateLimitFailureMode
	// Enabled is evaluated for every request. Nil preserves the default limit.
	Enabled func(*gin.Context) bool
}

var rateLimitScript = redis.NewScript(`
local current = redis.call('INCR', KEYS[1])
local ttl = redis.call('PTTL', KEYS[1])
local repaired = 0
if current == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
elseif ttl == -1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
  repaired = 1
end
return {current, repaired}
`)

// rateLimitPairScript checks two fixed-window buckets before incrementing
// either count. A rejected request consumes neither quota. When both limits
// are exhausted, the second bucket is reported so the caller can surface the
// user-wide limit.
var rateLimitPairScript = redis.NewScript(`
local function window_ttl(key)
  local ttl = redis.call('PTTL', key)
  if ttl == -1 then
    redis.call('PEXPIRE', key, ARGV[1])
    return tonumber(ARGV[1])
  end
  return ttl
end

local key_count = tonumber(redis.call('GET', KEYS[1]) or '0')
local user_count = tonumber(redis.call('GET', KEYS[2]) or '0')
local key_limit = tonumber(ARGV[2])
local user_limit = tonumber(ARGV[3])
local key_ttl = window_ttl(KEYS[1])
local user_ttl = window_ttl(KEYS[2])

if key_count >= key_limit or user_count >= user_limit then
  local blocked = 1
  if user_count >= user_limit then
    blocked = 2
  end
  return {0, blocked, key_count, user_count, key_ttl, user_ttl}
end

key_count = redis.call('INCR', KEYS[1])
if key_ttl == -2 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
user_count = redis.call('INCR', KEYS[2])
if user_ttl == -2 then
  redis.call('PEXPIRE', KEYS[2], ARGV[1])
end
return {1, 0, key_count, user_count, redis.call('PTTL', KEYS[1]), redis.call('PTTL', KEYS[2])}
`)

// rateLimitRun 允许测试覆写脚本执行逻辑
var rateLimitRun = func(ctx context.Context, client *redis.Client, key string, windowMillis int64) (int64, bool, error) {
	values, err := rateLimitScript.Run(ctx, client, []string{key}, windowMillis).Slice()
	if err != nil {
		return 0, false, err
	}
	if len(values) < 2 {
		return 0, false, fmt.Errorf("rate limit script returned %d values", len(values))
	}
	count, err := parseInt64(values[0])
	if err != nil {
		return 0, false, err
	}
	repaired, err := parseInt64(values[1])
	if err != nil {
		return 0, false, err
	}
	return count, repaired == 1, nil
}

// RateLimiter Redis 速率限制器
type RateLimiter struct {
	redis  *redis.Client
	prefix string
}

// NewRateLimiter 创建速率限制器实例
func NewRateLimiter(redisClient *redis.Client) *RateLimiter {
	return &RateLimiter{
		redis:  redisClient,
		prefix: "rate_limit:",
	}
}

// AllowResult 单次固定窗口限流判定结果。
type AllowResult struct {
	// Allowed 是否放行
	Allowed bool
	// Count 当前窗口内累计请求数（含本次）
	Count int64
	// RetryAfter 超限时距窗口重置的剩余时间（尽力而为；PTTL 不可用时回退为完整窗口）
	RetryAfter time.Duration
}

// PairAllowResult is the atomic result for two related fixed-window buckets.
// BlockedScope is "key" or "user" when Allowed is false.
type PairAllowResult struct {
	Allowed      bool
	BlockedScope string
	KeyCount     int64
	UserCount    int64
	RetryAfter   time.Duration
}

// Allow 对给定 key（不含 "rate_limit:" 前缀）执行一次固定窗口计数判定。
// 供需要自定义限流维度（如按用户 ID）的调用方使用；Redis 错误由调用方决定 fail-open/close。
func (r *RateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (AllowResult, error) {
	redisKey := r.prefix + key
	windowMillis := windowTTLMillis(window)

	count, repaired, err := rateLimitRun(ctx, r.redis, redisKey, windowMillis)
	if err != nil {
		return AllowResult{}, err
	}
	if repaired {
		log.Printf("[RateLimit] ttl repaired: key=%s window_ms=%d", redisKey, windowMillis)
	}

	result := AllowResult{Allowed: count <= int64(limit), Count: count}
	if !result.Allowed {
		result.RetryAfter = window
		if ttl, ttlErr := r.redis.PTTL(ctx, redisKey).Result(); ttlErr == nil && ttl > 0 {
			result.RetryAfter = ttl
		}
	}
	return result, nil
}

// AllowTogether checks and increments a per-key and a per-user bucket in one
// Redis Lua transaction. If either bucket is exhausted, neither count changes.
func (r *RateLimiter) AllowTogether(ctx context.Context, key string, keyLimit int, userKey string, userLimit int, window time.Duration) (PairAllowResult, error) {
	windowMillis := windowTTLMillis(window)
	values, err := rateLimitPairScript.Run(ctx, r.redis,
		[]string{r.prefix + key, r.prefix + userKey}, windowMillis, keyLimit, userLimit).Slice()
	if err != nil {
		return PairAllowResult{}, err
	}
	if len(values) < 6 {
		return PairAllowResult{}, fmt.Errorf("rate limit pair script returned %d values", len(values))
	}
	allowed, err := parseInt64(values[0])
	if err != nil {
		return PairAllowResult{}, err
	}
	blocked, err := parseInt64(values[1])
	if err != nil {
		return PairAllowResult{}, err
	}
	keyCount, err := parseInt64(values[2])
	if err != nil {
		return PairAllowResult{}, err
	}
	userCount, err := parseInt64(values[3])
	if err != nil {
		return PairAllowResult{}, err
	}
	keyTTL, err := parseInt64(values[4])
	if err != nil {
		return PairAllowResult{}, err
	}
	userTTL, err := parseInt64(values[5])
	if err != nil {
		return PairAllowResult{}, err
	}
	result := PairAllowResult{Allowed: allowed == 1, KeyCount: keyCount, UserCount: userCount}
	if result.Allowed {
		return result, nil
	}
	if blocked == 2 {
		result.BlockedScope = "user"
		result.RetryAfter = retryAfterFromPTTL(userTTL, window)
	} else {
		result.BlockedScope = "key"
		result.RetryAfter = retryAfterFromPTTL(keyTTL, window)
	}
	return result, nil
}

func retryAfterFromPTTL(ttl int64, fallback time.Duration) time.Duration {
	if ttl == 0 {
		return time.Second
	}
	if ttl > 0 {
		return time.Duration(ttl) * time.Millisecond
	}
	return fallback
}

// clientIPForRateLimit 返回 IP 维度限流使用的客户端地址。
// 与审计日志/会话绑定/API Key IP ACL 共用同一套安全客户端 IP 解析
// （SessionBindingContext 快照：兼容开关开启时信任反代转发头，关闭时走
// server.trusted_proxies 可信链）。避免默认反代部署下 Gin ClientIP 恒等于
// 代理地址、所有用户坍缩进同一个限流桶造成整体误拦截。
func clientIPForRateLimit(c *gin.Context) string {
	if resolved := ippkg.GetSecurityClientIP(c, false); resolved != "" {
		return resolved
	}
	return c.ClientIP()
}

// Limit 返回速率限制中间件
// key: 限制类型标识
// limit: 时间窗口内最大请求数
// window: 时间窗口
func (r *RateLimiter) Limit(key string, limit int, window time.Duration) gin.HandlerFunc {
	return r.LimitWithOptions(key, limit, window, RateLimitOptions{})
}

// LimitWithOptions 返回速率限制中间件（带可选配置）
func (r *RateLimiter) LimitWithOptions(key string, limit int, window time.Duration, opts RateLimitOptions) gin.HandlerFunc {
	failureMode := opts.FailureMode
	if failureMode != RateLimitFailClose {
		failureMode = RateLimitFailOpen
	}

	return func(c *gin.Context) {
		if opts.Enabled != nil && !opts.Enabled(c) {
			c.Next()
			return
		}
		result, err := r.Allow(c.Request.Context(), key+":"+clientIPForRateLimit(c), limit, window)
		if err != nil {
			log.Printf("[RateLimit] redis error: key=%s mode=%s err=%v", r.prefix+key, failureModeLabel(failureMode), err)
			if failureMode == RateLimitFailClose {
				abortRateLimit(c, window)
				return
			}
			// Redis 错误时放行，避免影响正常服务
			c.Next()
			return
		}

		// 超过限制
		if !result.Allowed {
			abortRateLimit(c, result.RetryAfter)
			return
		}

		c.Next()
	}
}

func windowTTLMillis(window time.Duration) int64 {
	ttl := window.Milliseconds()
	if ttl < 1 {
		return 1
	}
	return ttl
}

func abortRateLimit(c *gin.Context, retryAfter time.Duration) {
	if retryAfter > 0 {
		seconds := int64(retryAfter / time.Second)
		if retryAfter%time.Second > 0 {
			seconds++
		}
		c.Header("Retry-After", strconv.FormatInt(seconds, 10))
	}
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"error":   "rate limit exceeded",
		"message": "Too many requests, please try again later",
	})
}

func failureModeLabel(mode RateLimitFailureMode) string {
	if mode == RateLimitFailClose {
		return "fail-close"
	}
	return "fail-open"
}

func parseInt64(value any) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, err
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unexpected value type %T", value)
	}
}
