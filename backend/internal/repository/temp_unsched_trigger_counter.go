package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// tempUnschedTriggerIncrScript 在固定窗口内原子递增，并返回这一轮的 epoch。
// 第一次写入同时记下 epoch 和 TTL。窗口到期后两个键一起消失，下一次从 1 重新计数。
// epoch 缺失而计数还在时补一个新 epoch，不把已有计数清掉。
var tempUnschedTriggerIncrScript = redis.NewScript(`
	local key = KEYS[1]
	local epochKey = KEYS[2]
	local ttl = tonumber(ARGV[1])

	local count = redis.call('INCR', key)
	local epoch = redis.call('GET', epochKey)
	if count == 1 or not epoch then
		local now = redis.call('TIME')
		epoch = now[1] .. ':' .. now[2] .. ':' .. tostring(math.random(1000000, 9999999))
		local epochTTL = ttl
		if count ~= 1 then
			local remain = redis.call('TTL', key)
			if remain and remain > 0 then
				epochTTL = remain
			end
		else
			redis.call('EXPIRE', key, ttl)
		end
		redis.call('SET', epochKey, epoch, 'EX', epochTTL)
	end

	return {count, epoch}
`)

// tempUnschedTriggerResetScript 只删除仍属于这一轮的计数。
// 冷却写入之后如果又开了新一轮，旧 epoch 的 Reset 必须留下新计数。
var tempUnschedTriggerResetScript = redis.NewScript(`
	local key = KEYS[1]
	local epochKey = KEYS[2]
	local epoch = ARGV[1]
	local current = redis.call('GET', epochKey)
	if current and current == epoch then
		redis.call('DEL', key, epochKey)
		return 1
	end
	return 0
`)

type tempUnschedTriggerCounter struct {
	rdb *redis.Client
}

// NewTempUnschedTriggerCounter 创建跨实例共享的临时不可调度触发计数器。
// rdb 为空时返回 nil，调用方改用进程内计数。
func NewTempUnschedTriggerCounter(rdb *redis.Client) service.TempUnschedTriggerCounter {
	if rdb == nil {
		return nil
	}
	return &tempUnschedTriggerCounter{rdb: rdb}
}

func tempUnschedTriggerEpochKey(key string) string {
	return key + ":epoch"
}

func (c *tempUnschedTriggerCounter) Increment(ctx context.Context, key string, windowMinutes int) (int64, string, error) {
	if c == nil || c.rdb == nil {
		return 0, "", fmt.Errorf("temp unsched trigger counter is nil")
	}
	if windowMinutes < 1 {
		windowMinutes = 1
	}
	ttlSeconds := windowMinutes * 60
	if ttlSeconds < 60 {
		ttlSeconds = 60
	}
	values, err := tempUnschedTriggerIncrScript.Run(ctx, c.rdb, []string{key, tempUnschedTriggerEpochKey(key)}, ttlSeconds).Slice()
	if err != nil {
		return 0, "", fmt.Errorf("increment temp unsched trigger count: %w", err)
	}
	if len(values) != 2 {
		return 0, "", fmt.Errorf("increment temp unsched trigger count: unexpected redis reply")
	}
	count, err := redisReplyInt64(values[0])
	if err != nil {
		return 0, "", fmt.Errorf("increment temp unsched trigger count: %w", err)
	}
	epoch, err := redisReplyString(values[1])
	if err != nil || epoch == "" {
		return 0, "", fmt.Errorf("increment temp unsched trigger count: missing epoch")
	}
	return count, epoch, nil
}

func (c *tempUnschedTriggerCounter) Reset(ctx context.Context, key string, epoch string) error {
	if c == nil || c.rdb == nil {
		return fmt.Errorf("temp unsched trigger counter is nil")
	}
	if epoch == "" {
		return nil
	}
	if err := tempUnschedTriggerResetScript.Run(ctx, c.rdb, []string{key, tempUnschedTriggerEpochKey(key)}, epoch).Err(); err != nil {
		return fmt.Errorf("reset temp unsched trigger count: %w", err)
	}
	return nil
}

func redisReplyInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case string:
		var parsed int64
		_, err := fmt.Sscan(typed, &parsed)
		return parsed, err
	default:
		return 0, fmt.Errorf("unexpected integer reply %T", value)
	}
}

func redisReplyString(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case []byte:
		return string(typed), nil
	default:
		return "", fmt.Errorf("unexpected string reply %T", value)
	}
}
