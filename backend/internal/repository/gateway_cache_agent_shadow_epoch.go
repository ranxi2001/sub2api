package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// No customer content. A separate bounded metadata budget spans all of a
// principal's sessions. Eviction loses future recovery, never owner authority.
func openAIAgentShadowEpochKeys(key string) (string, string, error) {
	parts := strings.Split(key, ":")
	if len(parts) != 2 {
		return "", "", errors.New("invalid association scope")
	}
	for _, part := range parts {
		if _, err := openAIAgentRecoveryKey(part); err != nil {
			return "", "", err
		}
	}
	scope := "{" + parts[0] + "}"
	return "openai_agent_shadow_epoch:v1:" + scope + ":" + parts[1], "openai_agent_shadow_index:v1:" + scope, nil
}

var reserveOpenAIAgentShadowEpoch = redis.NewScript(`
local now = redis.call('TIME')
local millis = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', millis)
for _, key in ipairs(expired) do redis.call('DEL', key); redis.call('ZREM', KEYS[2], key) end
local old = redis.call('GET', KEYS[1])
local ttl = math.max(tonumber(ARGV[2]), redis.call('PTTL', KEYS[1]))
if old == ARGV[1] then return 1 end
if not old then
 while redis.call('ZCARD', KEYS[2]) >= 128 do
  local oldest = redis.call('ZRANGE', KEYS[2], 0, 0)
  redis.call('DEL', oldest[1]); redis.call('ZREM', KEYS[2], oldest[1])
 end
end
redis.call('ZADD', KEYS[2], millis + ttl, KEYS[1])
redis.call('PEXPIRE', KEYS[2], math.max(ttl, redis.call('PTTL', KEYS[2])))
if old and (old == 'conflict' or string.sub(old, 1, 8) == 'pending:') then
 redis.call('SET', KEYS[1], 'conflict', 'PX', ttl)
 return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ttl)
return 1
`)

var finishOpenAIAgentShadowEpoch = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
local value = 'failed:' .. ARGV[1]
if ARGV[2] == '1' then value = 'ready:' .. ARGV[1] end
redis.call('SET', KEYS[1], value, 'PX', math.max(tonumber(ARGV[3]), redis.call('PTTL', KEYS[1])))
local now = redis.call('TIME')
local millis = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
redis.call('ZADD', KEYS[2], millis + redis.call('PTTL', KEYS[1]), KEYS[1])
redis.call('PEXPIRE', KEYS[2], math.max(tonumber(ARGV[3]), redis.call('PTTL', KEYS[2])))
return 1
`)

func (c *gatewayCache) ReserveOpenAIAgentRecoveryEpoch(ctx context.Context, key, token string, ttl time.Duration) error {
	ttl = min(ttl, service.OpenAIReadableShadowTTL)
	key, index, err := openAIAgentShadowEpochKeys(key)
	if err != nil {
		return err
	}
	// A successfully stored conflict revokes shadow eligibility; it must not
	// change an otherwise valid original response or its owner semantics.
	return reserveOpenAIAgentShadowEpoch.Run(ctx, c.rdb, []string{key, index}, token, ttl.Milliseconds()).Err()
}

func (c *gatewayCache) FinishOpenAIAgentRecoveryEpoch(ctx context.Context, key, token string, completed bool, ttl time.Duration) error {
	ttl = min(ttl, service.OpenAIReadableShadowTTL)
	key, index, err := openAIAgentShadowEpochKeys(key)
	if err != nil {
		return err
	}
	flag := 0
	if completed {
		flag = 1
	}
	return finishOpenAIAgentShadowEpoch.Run(ctx, c.rdb, []string{key, index}, token, flag, ttl.Milliseconds()).Err()
}

func (c *gatewayCache) GetOpenAIAgentRecoveryEpoch(ctx context.Context, key string) (string, error) {
	key, _, err := openAIAgentShadowEpochKeys(key)
	if err != nil {
		return "", err
	}
	value, err := c.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return value, err
}

var _ service.OpenAIAgentRecoveryEpochCache = (*gatewayCache)(nil)
