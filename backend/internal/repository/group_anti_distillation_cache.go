package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const groupAntiDistillationStateTTL = 24 * time.Hour

type groupAntiDistillationCache struct {
	rdb *redis.Client
}

func NewGroupAntiDistillationCache(rdb *redis.Client) service.GroupAntiDistillationCache {
	return &groupAntiDistillationCache{rdb: rdb}
}

func groupAntiDistillationRedisKeys(userID int64) []string {
	// All keys share a Redis hash slot; no API key, email or request body is kept.
	base := "group-anti-distillation:{" + strconv.FormatInt(userID, 10) + "}:"
	return []string{base + "samples", base + "seen", base + "pause", base + "last-pause-until"}
}

func (c *groupAntiDistillationCache) PauseRemaining(ctx context.Context, userID int64) (time.Duration, error) {
	if c == nil || c.rdb == nil {
		return 0, errors.New("group protection Redis unavailable")
	}
	remaining, err := c.rdb.PTTL(ctx, groupAntiDistillationRedisKeys(userID)[2]).Result()
	if err != nil {
		return 0, err
	}
	if remaining < 0 {
		return 0, nil
	}
	return remaining, nil
}

// A single Lua operation owns deduplication, sample insertion and pause creation.
// Completion order defines the last 20 valid requests. A pause clears the old
// window, never extends an existing pause, and ignores all in-flight completions
// started before that pause ended. Idle sample/seen state expires after 24h.
var groupAntiDistillationObserveScript = redis.NewScript(`
local remaining = redis.call('PTTL', KEYS[3])
if remaining > 0 then return {0, remaining, 0} end
local started = tonumber(ARGV[5])
local last_until = tonumber(redis.call('GET', KEYS[4]) or '0')
if started > 0 and started <= last_until then return {0, 0, 0} end
if redis.call('ZSCORE', KEYS[2], ARGV[1]) then return {0, 0, 0} end
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
redis.call('ZADD', KEYS[2], now, ARGV[1])
redis.call('ZREMRANGEBYRANK', KEYS[2], 0, -257)
redis.call('PEXPIRE', KEYS[2], ARGV[4])
redis.call('LPUSH', KEYS[1], ARGV[2])
redis.call('LTRIM', KEYS[1], 0, 19)
redis.call('PEXPIRE', KEYS[1], ARGV[4])
if redis.call('LLEN', KEYS[1]) < 20 then return {0, 0, 0} end
local zeros = 0
for _, sample in ipairs(redis.call('LRANGE', KEYS[1], 0, 19)) do
  zeros = zeros + tonumber(sample)
end
if zeros < 18 then return {0, 0, 0} end
local pause = tonumber(ARGV[3])
local until_ms = now + pause
local established = redis.call('SET', KEYS[3], ARGV[1], 'PX', pause, 'NX')
if not established then return {0, 0, 0} end
redis.call('SET', KEYS[4], until_ms, 'PX', pause + tonumber(ARGV[4]))
redis.call('DEL', KEYS[1])
return {1, pause, until_ms}
`)

func (c *groupAntiDistillationCache) Observe(ctx context.Context, userID int64, requestID string, zero bool, pause time.Duration, startedAt time.Time) (bool, time.Time, error) {
	if c == nil || c.rdb == nil {
		return false, time.Time{}, errors.New("group protection Redis unavailable")
	}
	digest := sha256.Sum256([]byte(requestID))
	zeroSample := 0
	if zero {
		zeroSample = 1
	}
	var startedMS int64
	if !startedAt.IsZero() {
		startedMS = startedAt.UnixMilli()
	}
	values, err := groupAntiDistillationObserveScript.Run(ctx, c.rdb, groupAntiDistillationRedisKeys(userID),
		hex.EncodeToString(digest[:]), zeroSample, pause.Milliseconds(), groupAntiDistillationStateTTL.Milliseconds(), startedMS).Int64Slice()
	if err != nil {
		return false, time.Time{}, err
	}
	if len(values) != 3 {
		return false, time.Time{}, errors.New("invalid group protection result")
	}
	return values[0] == 1, time.UnixMilli(values[2]), nil
}
