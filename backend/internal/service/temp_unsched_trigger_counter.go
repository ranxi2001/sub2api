package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// memoryTempUnschedTriggerCounter 是单进程固定窗口计数器。
// Redis 不可用时使用它，测试也可以注入可控时钟。
type memoryTempUnschedTriggerCounter struct {
	mu    sync.Mutex
	now   func() time.Time
	items map[string]tempUnschedTriggerEntry
	seq   int64
}

type tempUnschedTriggerEntry struct {
	count   int64
	expires time.Time
	epoch   string
}

func newMemoryTempUnschedTriggerCounter(now func() time.Time) *memoryTempUnschedTriggerCounter {
	if now == nil {
		now = time.Now
	}
	return &memoryTempUnschedTriggerCounter{
		now:   now,
		items: make(map[string]tempUnschedTriggerEntry),
	}
}

func (c *memoryTempUnschedTriggerCounter) Increment(_ context.Context, key string, windowMinutes int) (int64, string, error) {
	if c == nil {
		return 0, "", fmt.Errorf("temp unsched trigger counter is nil")
	}
	if windowMinutes < 1 {
		windowMinutes = 1
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok || !entry.expires.After(now) {
		c.seq++
		entry = tempUnschedTriggerEntry{
			count:   1,
			expires: now.Add(time.Duration(windowMinutes) * time.Minute),
			epoch:   fmt.Sprintf("%d:%d", now.UnixNano(), c.seq),
		}
		c.items[key] = entry
		return entry.count, entry.epoch, nil
	}
	entry.count++
	c.items[key] = entry
	return entry.count, entry.epoch, nil
}

func (c *memoryTempUnschedTriggerCounter) Reset(_ context.Context, key string, epoch string) error {
	if c == nil {
		return fmt.Errorf("temp unsched trigger counter is nil")
	}
	if epoch == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok || entry.epoch != epoch {
		return nil
	}
	delete(c.items, key)
	return nil
}

func tempUnschedTriggerCounterKey(accountID int64, ruleIndex int, modelScoped bool, modelKey string) string {
	if modelScoped {
		modelKey = strings.TrimSpace(modelKey)
		if modelKey != "" {
			return fmt.Sprintf("temp_unsched_trigger:%d:%d:model:%s", accountID, ruleIndex, modelKey)
		}
	}
	return fmt.Sprintf("temp_unsched_trigger:%d:%d:account", accountID, ruleIndex)
}
