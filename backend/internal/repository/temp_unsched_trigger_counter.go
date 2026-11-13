package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// tempUnschedTriggerIncrScript 在固定窗口内原子递增。
// 第一次写入设置 TTL，窗口到期后键消失，下一次从 1 重新计数。
var tempUnschedTriggerIncrScript = redis.NewScript(`
	local key = KEYS[1]
	local ttl = tonumber(ARGV[1])

	local count = redis.call('INCR', key)
	if count == 1 then
		redis.call('EXPIRE', key, ttl)
	end

	return count
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

func (c *tempUnschedTriggerCounter) Increment(ctx context.Context, key string, windowMinutes int) (int64, error) {
	if c == nil || c.rdb == nil {
		return 0, fmt.Errorf("temp unsched trigger counter is nil")
	}
	if windowMinutes < 1 {
		windowMinutes = 1
	}
	ttlSeconds := windowMinutes * 60
	if ttlSeconds < 60 {
		ttlSeconds = 60
	}
	count, err := tempUnschedTriggerIncrScript.Run(ctx, c.rdb, []string{key}, ttlSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment temp unsched trigger count: %w", err)
	}
	return count, nil
}

func (c *tempUnschedTriggerCounter) Reset(ctx context.Context, key string) error {
	if c == nil || c.rdb == nil {
		return fmt.Errorf("temp unsched trigger counter is nil")
	}
	if err := c.rdb.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("reset temp unsched trigger count: %w", err)
	}
	return nil
}
