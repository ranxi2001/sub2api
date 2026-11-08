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
