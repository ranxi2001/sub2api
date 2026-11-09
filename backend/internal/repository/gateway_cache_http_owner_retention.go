package repository

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Validate all three representations before changing any deadline. Retention
// never restores a missing legacy mirror, upgrades a legacy-only record, or
// overwrites a user/key changed since the caller's ownership read.
var refreshHTTPResponseOwnerScript = redis.NewScript(`
local current = redis.call('MGET', KEYS[1], KEYS[2], KEYS[3])
for i = 1, 3 do
  if current[i] ~= ARGV[i] then return 0 end
end
for i = 1, 3 do
  local remaining = redis.call('PTTL', KEYS[i])
  if remaining >= 0 and remaining < tonumber(ARGV[4]) then
    redis.call('PEXPIRE', KEYS[i], ARGV[4])
  end
end
return 1
`)

func (c *gatewayCache) RefreshHTTPResponseOwner(ctx context.Context, groupID int64, responseID string, ownerUserID, ownerAPIKeyID int64, ttl time.Duration) (bool, error) {
	if c == nil || c.rdb == nil || groupID < 0 || strings.TrimSpace(responseID) == "" || ownerUserID <= 0 || ownerAPIKeyID <= 0 || ttl.Milliseconds() <= 0 {
		return false, errHTTPResponseOwnerUnavailable
	}
	user, key := strconv.FormatInt(ownerUserID, 10), strconv.FormatInt(ownerAPIKeyID, 10)
	result, err := refreshHTTPResponseOwnerScript.Run(ctx, c.rdb, httpResponseOwnerKeys(groupID, responseID), "v1:"+user+":"+key, user, key, ttl.Milliseconds()).Int()
	if err != nil {
		return false, errHTTPResponseOwnerUnavailable
	}
	return result == 1, nil
}

var _ service.OpenAIHTTPResponseOwnerRetentionCache = (*gatewayCache)(nil)
