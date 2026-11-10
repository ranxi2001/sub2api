package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// One bounded, atomic probe returns no account JSON or credentials. A probe is
// not proof of eligibility, JSON validity, outbox freshness or upstream health.
// A unique explicit empty-snapshot member represents a published empty pool;
// an absent snapshot or mixed/corrupt membership remains unready.
const schedulerReadinessMaxAccounts = 4096

var schedulerReadinessScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[4]) == 1 or redis.call('GET', KEYS[1]) ~= '1' then return 0 end
local active = redis.call('GET', KEYS[2])
local stamp = redis.call('HMGET', KEYS[3], 'version', 'published_ms')
if active == false or active == '' or stamp[1] ~= active then return 0 end
local published = tonumber(stamp[2])
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
if published == nil or published > now or now - published > tonumber(ARGV[1]) then return 0 end
local snapshot = ARGV[2] .. active
local count = redis.call('ZCARD', snapshot)
if count < 1 or count > tonumber(ARGV[3]) then return 0 end
local ids = redis.call('ZRANGE', snapshot, 0, -1)
if count == 1 and ids[1] == ARGV[6] then return 1 end
for _, id in ipairs(ids) do
    if string.match(id, '^[1-9][0-9]*$') == nil then return 0 end
    local key = ARGV[4] .. id
    if redis.call('TYPE', key).ok ~= 'string' or redis.call('STRLEN', key) == 0 then return 0 end
    local full = ARGV[5] .. id
    if redis.call('TYPE', full).ok ~= 'string' or redis.call('STRLEN', full) == 0 then return 0 end
end
return 1
`)

func (c *schedulerCache) CheckSnapshotReadiness(ctx context.Context, buckets []service.SchedulerBucket, maxAge time.Duration) error {
	if c == nil || c.rdb == nil || len(buckets) == 0 || len(buckets) > 16 || maxAge < time.Millisecond || maxAge > 24*time.Hour {
		return service.ErrSchedulerCacheNotReady
	}
	for _, bucket := range buckets {
		if err := ctx.Err(); err != nil {
			return err
		}
		keys := []string{
			schedulerBucketKey(schedulerReadyPrefix, bucket),
			schedulerBucketKey(schedulerActivePrefix, bucket),
			schedulerBucketKey(schedulerPublishedPrefix, bucket),
			schedulerBucketKey(schedulerRetiredPrefix, bucket),
		}
		prefix := schedulerSnapshotKey(bucket, "")
		result, err := schedulerReadinessScript.Run(ctx, c.rdb, keys,
			maxAge.Milliseconds(), prefix, schedulerReadinessMaxAccounts, schedulerAccountMetaPrefix, schedulerAccountPrefix, schedulerEmptySnapshotMember).Int64()
		if err != nil || result != 1 {
			// Redis errors may contain private topology; health callers only get
			// this stable, non-secret error and never initiate a rebuild/DB read.
			return service.ErrSchedulerCacheNotReady
		}
	}
	return nil
}
