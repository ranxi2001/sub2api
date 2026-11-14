package repository

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Both keys and every indexed member share the principal's Redis Cluster slot.
// Index cardinality is bounded even when callers continually change sessions.
func openAIReadableShadowKeys(principal, task string) (string, string, error) {
	for _, value := range []string{principal, task} {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 32 {
			return "", "", errors.New("invalid readable shadow key")
		}
	}
	scope := "{" + principal + "}"
	return "openai_readable_shadow:v1:" + scope + ":" + task, "openai_readable_shadow_index:v1:" + scope, nil
}

var putOpenAIReadableShadowScript = redis.NewScript(`
local now = redis.call('TIME')
local millis = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
local ttl = tonumber(ARGV[2])
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', millis)
for _, key in ipairs(expired) do
 redis.call('DEL', key)
 redis.call('ZREM', KEYS[2], key)
end
local old = redis.call('GET', KEYS[1])
if old then
 if old ~= ARGV[1] then
  local remaining = redis.call('PTTL', KEYS[1])
  if remaining > 0 then redis.call('SET', KEYS[1], 'conflict', 'PX', remaining) end
 end
 return 0
end
while redis.call('ZCARD', KEYS[2]) >= tonumber(ARGV[3]) do
 local oldest = redis.call('ZRANGE', KEYS[2], 0, 0)
 redis.call('DEL', oldest[1])
 redis.call('ZREM', KEYS[2], oldest[1])
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ttl)
redis.call('ZADD', KEYS[2], millis + ttl, KEYS[1])
redis.call('PEXPIRE', KEYS[2], math.max(ttl, redis.call('PTTL', KEYS[2])))
return 1
`)

func (c *gatewayCache) PutOpenAIReadableShadow(ctx context.Context, principal, task, value string, ttl time.Duration) error {
	key, index, err := openAIReadableShadowKeys(principal, task)
	if err != nil {
		return err
	}
	if value == "" || len(value) > service.OpenAIReadableShadowMaxBytes || ttl <= 0 || ttl > service.OpenAIReadableShadowTTL {
		return errors.New("invalid readable shadow publication")
	}
	return putOpenAIReadableShadowScript.Run(ctx, c.rdb, []string{key, index}, value, ttl.Milliseconds(), service.OpenAIReadableShadowMaxRecords).Err()
}

func (c *gatewayCache) GetOpenAIReadableShadow(ctx context.Context, principal, task string) (string, error) {
	key, _, err := openAIReadableShadowKeys(principal, task)
	if err != nil {
		return "", err
	}
	value, err := c.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return value, err
}

var invalidateOpenAIReadableShadowScript = redis.NewScript(`
local remaining = redis.call('PTTL', KEYS[1])
if remaining > 0 then
 redis.call('SET', KEYS[1], 'conflict', 'PX', remaining)
end
return 1
`)

func (c *gatewayCache) InvalidateOpenAIReadableShadow(ctx context.Context, principal, task string) error {
	key, _, err := openAIReadableShadowKeys(principal, task)
	if err != nil {
		return err
	}
	return invalidateOpenAIReadableShadowScript.Run(ctx, c.rdb, []string{key}).Err()
}

var _ service.OpenAIReadableShadowCache = (*gatewayCache)(nil)
