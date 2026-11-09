package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Compare the integer as text: Lua numbers cannot represent every int64 account
// ID exactly. Never SET a missing key or shorten an existing/persistent record.
var refreshSessionAccountIDScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
local remaining = redis.call('PTTL', KEYS[1])
if remaining >= 0 and remaining < tonumber(ARGV[2]) then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 1
`)

func (c *gatewayCache) RefreshSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) (bool, error) {
	if strings.TrimSpace(sessionHash) == "" || accountID <= 0 || ttl.Milliseconds() <= 0 {
		return false, errors.New("invalid response ownership retention")
	}
	result, err := refreshSessionAccountIDScript.Run(ctx, c.rdb, []string{buildSessionKey(groupID, sessionHash)}, accountID, ttl.Milliseconds()).Int()
	return result == 1, err
}

var _ service.OpenAIWSResponseAccountRetentionCache = (*gatewayCache)(nil)
