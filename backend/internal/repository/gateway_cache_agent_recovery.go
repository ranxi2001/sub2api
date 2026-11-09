package repository

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const openAIAgentRecoveryPrefix = "openai_agent_owner:v1:"

// Each key is independent, so this also works on Redis Cluster. Conflicting
// observations are never last-writer-wins. Refreshing a conflict cannot shorten
// its existing retention and cannot turn it back into a successful grant.
var publishOpenAIAgentRecoveryScript = redis.NewScript(`
local old = redis.call('GET', KEYS[1])
local ttl = tonumber(ARGV[2])
local remaining = redis.call('PTTL', KEYS[1])
if remaining > ttl then ttl = remaining end
if old and old ~= ARGV[1] then
  redis.call('SET', KEYS[1], 'conflict', 'PX', ttl)
else
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ttl)
end
return 1
`)

func openAIAgentRecoveryKey(key string) (string, error) {
	decoded, err := hex.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("invalid encrypted agent ownership key")
	}
	return openAIAgentRecoveryPrefix + key, nil
}

func (c *gatewayCache) GetOpenAIAgentRecovery(ctx context.Context, key string) (string, error) {
	redisKey, err := openAIAgentRecoveryKey(key)
	if err != nil {
		return "", err
	}
	value, err := c.rdb.Get(ctx, redisKey).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return value, err
}

func (c *gatewayCache) PublishOpenAIAgentRecovery(ctx context.Context, key, route string, ttl time.Duration) error {
	redisKey, err := openAIAgentRecoveryKey(key)
	if err != nil {
		return err
	}
	if route == "" || ttl.Milliseconds() <= 0 {
		return errors.New("invalid encrypted agent ownership publication")
	}
	return publishOpenAIAgentRecoveryScript.Run(ctx, c.rdb, []string{redisKey}, route, ttl.Milliseconds()).Err()
}

var _ service.OpenAIAgentRecoveryCache = (*gatewayCache)(nil)

// Waiting for quota does not constitute another successful observation. Only
// extend the existing exact route; expiry or a concurrent delivery/conflict must
// never be overwritten or resurrected by a delayed waiting request.
var refreshOpenAIAgentRecoveryScript = redis.NewScript(`
local old = redis.call('GET', KEYS[1])
if not old or old ~= ARGV[1] or old == 'failed' or old == 'conflict' or string.sub(old, 1, 8) == 'pending:' then
 return 0
end
local remaining = redis.call('PTTL', KEYS[1])
local ttl = tonumber(ARGV[2])
if remaining >= 0 and remaining < ttl then
 redis.call('PEXPIRE', KEYS[1], ttl)
end
return 1
`)

func (c *gatewayCache) RefreshOpenAIAgentRecovery(ctx context.Context, key, route string, ttl time.Duration) (bool, error) {
	redisKey, err := openAIAgentRecoveryKey(key)
	if err != nil {
		return false, err
	}
	if route == "" || ttl.Milliseconds() <= 0 {
		return false, errors.New("invalid encrypted agent ownership refresh")
	}
	result, err := refreshOpenAIAgentRecoveryScript.Run(ctx, c.rdb, []string{redisKey}, route, ttl.Milliseconds()).Int()
	return result == 1 && err == nil, err
}

var _ service.OpenAIAgentRecoveryRetentionCache = (*gatewayCache)(nil)

// A producer token is compared atomically on the same Redis Cluster key as the
// completed route. Neither abort nor a delayed commit may replace another token.
var reserveOpenAIAgentRecoveryScript = redis.NewScript(`
local old = redis.call('GET', KEYS[1])
local ttl = math.max(tonumber(ARGV[3]), redis.call('PTTL', KEYS[1]))
if not old or old == ARGV[2] then
 redis.call('SET', KEYS[1], ARGV[2], 'PX', ttl)
 return 1
end
if old == ARGV[1] then return 1 end
redis.call('SET', KEYS[1], 'conflict', 'PX', ttl)
return 0
`)

var finishOpenAIAgentRecoveryScript = redis.NewScript(`
local old = redis.call('GET', KEYS[1])
local ttl = math.max(tonumber(ARGV[4]), redis.call('PTTL', KEYS[1]))
if old == ARGV[2] then
 local value = 'failed'
 if ARGV[3] == '1' then value = ARGV[1] end
 redis.call('SET', KEYS[1], value, 'PX', ttl)
 return 1
end
if old == ARGV[1] then return 1 end
return 0
`)

func (c *gatewayCache) ReserveOpenAIAgentRecovery(ctx context.Context, key, route, pending string, ttl time.Duration) error {
	redisKey, err := openAIAgentRecoveryKey(key)
	if err != nil {
		return err
	}
	if route == "" || pending == "" || ttl.Milliseconds() <= 0 {
		return errors.New("invalid encrypted agent reservation")
	}
	result, err := reserveOpenAIAgentRecoveryScript.Run(ctx, c.rdb, []string{redisKey}, route, pending, ttl.Milliseconds()).Int()
	if err == nil && result != 1 {
		return errors.New("encrypted agent reservation conflict")
	}
	return err
}

func (c *gatewayCache) FinishOpenAIAgentRecovery(ctx context.Context, key, route, pending string, commit bool, ttl time.Duration) error {
	redisKey, err := openAIAgentRecoveryKey(key)
	if err != nil {
		return err
	}
	if route == "" || pending == "" || ttl.Milliseconds() <= 0 {
		return errors.New("invalid encrypted agent commit")
	}
	flag := 0
	if commit {
		flag = 1
	}
	result, err := finishOpenAIAgentRecoveryScript.Run(ctx, c.rdb, []string{redisKey}, route, pending, flag, ttl.Milliseconds()).Int()
	if err == nil && result != 1 {
		return errors.New("encrypted agent reservation no longer owned")
	}
	return err
}

var _ service.OpenAIAgentRecoveryDeliveryCache = (*gatewayCache)(nil)
