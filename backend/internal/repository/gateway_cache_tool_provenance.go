package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

func openAIToolProvenanceKeys(scope string) (string, string, error) {
	key, index, err := openAIAgentShadowEpochKeys(scope)
	return strings.Replace(key, "openai_agent_shadow_epoch:", "openai_tool_provenance:", 1), strings.Replace(index, "openai_agent_shadow_index:", "openai_tool_provenance_index:", 1), err
}

// A call ID cannot be reissued by another delivery and inherit its old grant.
// Budget/TTL eviction loses recoverability, never creates proof.
var reserveOpenAIToolProvenance = redis.NewScript(`
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
local value = ARGV[1]
if old then value = 'conflict' end
redis.call('SET', KEYS[1], value, 'PX', ttl)
redis.call('ZADD', KEYS[2], millis + ttl, KEYS[1])
redis.call('PEXPIRE', KEYS[2], math.max(ttl, redis.call('PTTL', KEYS[2])))
return 1
`)

var finishOpenAIToolProvenance = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
local value = 'failed'
if ARGV[3] == '1' then value = ARGV[2] end
local ttl = math.max(tonumber(ARGV[4]), redis.call('PTTL', KEYS[1]))
redis.call('SET', KEYS[1], value, 'PX', ttl)
local now = redis.call('TIME')
local millis = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
redis.call('ZADD', KEYS[2], millis + ttl, KEYS[1])
redis.call('PEXPIRE', KEYS[2], math.max(ttl, redis.call('PTTL', KEYS[2])))
return 1
`)

func (c *gatewayCache) GetOpenAIToolProvenance(ctx context.Context, scope string) (string, error) {
	key, _, err := openAIToolProvenanceKeys(scope)
	if err != nil {
		return "", err
	}
	value, err := c.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return value, err
}
func (c *gatewayCache) ReserveOpenAIToolProvenance(ctx context.Context, scope, token string, ttl time.Duration) error {
	key, index, err := openAIToolProvenanceKeys(scope)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(token, "pending:") || len(token) > 1024 || ttl <= 0 {
		return errors.New("invalid tool provenance reservation")
	}
	return reserveOpenAIToolProvenance.Run(ctx, c.rdb, []string{key, index}, token, min(ttl, service.OpenAIReadableShadowTTL).Milliseconds()).Err()
}
func (c *gatewayCache) FinishOpenAIToolProvenance(ctx context.Context, scope, token, record string, completed bool, ttl time.Duration) error {
	key, index, err := openAIToolProvenanceKeys(scope)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(token, "pending:") || len(token) > 1024 || len(record) > service.OpenAIToolProvenanceMaxBytes || ttl <= 0 {
		return errors.New("invalid tool provenance completion")
	}
	flag := 0
	if completed {
		flag = 1
	}
	return finishOpenAIToolProvenance.Run(ctx, c.rdb, []string{key, index}, token, record, flag, min(ttl, service.OpenAIReadableShadowTTL).Milliseconds()).Err()
}

var _ service.OpenAIToolProvenanceCache = (*gatewayCache)(nil)

// Bind a discovery receipt to an existing delivered-call record atomically.
// Missing/expired/changed records never create a fresh catalog grant.
var compareAndSwapOpenAIToolProvenance = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
local ttl = redis.call('PTTL', KEYS[1])
if ttl <= 0 then return 0 end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ttl)
return 1
`)

func (c *gatewayCache) CompareAndSwapOpenAIToolProvenance(ctx context.Context, scope, old, next string, ttl time.Duration) (bool, error) {
	key, _, err := openAIToolProvenanceKeys(scope)
	if err != nil {
		return false, err
	}
	if old == "" || next == "" || len(next) > service.OpenAIToolProvenanceMaxBytes || ttl <= 0 {
		return false, errors.New("invalid discovery provenance update")
	}
	updated, err := compareAndSwapOpenAIToolProvenance.Run(ctx, c.rdb, []string{key}, old, next).Int()
	return updated == 1, err
}

var _ service.OpenAIDiscoveryProvenanceCache = (*gatewayCache)(nil)
