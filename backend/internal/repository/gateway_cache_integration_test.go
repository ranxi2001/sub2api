//go:build integration

package repository

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type GatewayCacheSuite struct {
	IntegrationRedisSuite
	cache service.GatewayCache
}

func (s *GatewayCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.cache = NewGatewayCache(s.rdb)
}

func (s *GatewayCacheSuite) TestHTTPResponseOwnerSharedRecord() {
	t := s.T()
	// Two independent stores use the actual Redis adapter, not a local map.
	writer := service.NewOpenAIWSStateStore(NewGatewayCache(s.rdb))
	reader := service.NewOpenAIWSStateStore(NewGatewayCache(s.rdb))
	require.NoError(t, writer.BindHTTPResponseOwner(s.ctx, 17, "resp_shared_fixture", 101, 201, time.Minute))
	user, key, found, err := reader.GetHTTPResponseOwner(s.ctx, 17, "resp_shared_fixture")
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 101, user)
	require.EqualValues(t, 201, key)
	keys := httpResponseOwnerKeys(17, "resp_shared_fixture")
	for _, ownerKey := range keys {
		ttl, err := s.rdb.PTTL(s.ctx, ownerKey).Result()
		require.NoError(t, err)
		require.Greater(t, ttl, time.Duration(0))
		require.LessOrEqual(t, ttl, time.Minute)
	}
	require.NoError(t, s.rdb.Set(s.ctx, keys[0], "v99:101:201", time.Minute).Err())
	_, _, found, err = writer.GetHTTPResponseOwner(s.ctx, 17, "resp_shared_fixture")
	require.ErrorIs(t, err, errHTTPResponseOwnerUnavailable)
	require.False(t, found, "a warm writer must honor authoritative version rejection")
	require.NoError(t, s.rdb.Del(s.ctx, keys...).Err())
	_, _, found, err = writer.GetHTTPResponseOwner(s.ctx, 17, "resp_shared_fixture")
	require.NoError(t, err)
	require.False(t, found)
}

func (s *GatewayCacheSuite) TestWSNodeOwnerFencesForeignBoot() {
	cache := &gatewayCache{rdb: s.rdb}
	ref := service.WSNodeOwnerRef{GroupID: 17, APIKeyID: 101, AccountID: 201, ResponseID: "resp_ws_fixture"}
	claimed, err := cache.ClaimWSNodeOwner(s.ctx, ref, "v1:node-a:boot-a:conn-digest-a", time.Minute)
	require.NoError(s.T(), err)
	require.True(s.T(), claimed)
	claimed, err = cache.ClaimWSNodeOwner(s.ctx, ref, "v1:node-a:boot-b:conn-digest-a", time.Minute)
	require.NoError(s.T(), err)
	require.False(s.T(), claimed)
	matched, err := cache.MatchesWSNodeOwner(s.ctx, ref, "v1:node-a:boot-a:conn-digest-a")
	require.NoError(s.T(), err)
	require.True(s.T(), matched)
	require.NoError(s.T(), s.rdb.Del(s.ctx, wsNodeOwnerKey(ref)).Err())
	matched, err = cache.MatchesWSNodeOwner(s.ctx, ref, "v1:node-a:boot-a:conn-digest-a")
	require.NoError(s.T(), err)
	require.False(s.T(), matched)
}

func (s *GatewayCacheSuite) TestGetSessionAccountID_Missing() {
	_, err := s.cache.GetSessionAccountID(s.ctx, 1, "nonexistent")
	require.True(s.T(), errors.Is(err, service.ErrStickySessionNotFound), "expected ErrStickySessionNotFound for missing session")
}

func (s *GatewayCacheSuite) TestSetAndGetSessionAccountID() {
	sessionID := "s1"
	accountID := int64(99)
	groupID := int64(1)
	sessionTTL := 1 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, accountID, sessionTTL), "SetSessionAccountID")

	sid, err := s.cache.GetSessionAccountID(s.ctx, groupID, sessionID)
	require.NoError(s.T(), err, "GetSessionAccountID")
	require.Equal(s.T(), accountID, sid, "session id mismatch")
}

func (s *GatewayCacheSuite) TestSessionAccountID_TTL() {
	sessionID := "s2"
	accountID := int64(100)
	groupID := int64(1)
	sessionTTL := 1 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, accountID, sessionTTL), "SetSessionAccountID")

	sessionKey := buildSessionKey(groupID, sessionID)
	ttl, err := s.rdb.TTL(s.ctx, sessionKey).Result()
	require.NoError(s.T(), err, "TTL sessionKey after Set")
	s.AssertTTLWithin(ttl, 1*time.Second, sessionTTL)
}

func (s *GatewayCacheSuite) TestRefreshSessionTTL() {
	sessionID := "s3"
	accountID := int64(101)
	groupID := int64(1)
	initialTTL := 1 * time.Minute
	refreshTTL := 3 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, accountID, initialTTL), "SetSessionAccountID")

	require.NoError(s.T(), s.cache.RefreshSessionTTL(s.ctx, groupID, sessionID, refreshTTL), "RefreshSessionTTL")

	sessionKey := buildSessionKey(groupID, sessionID)
	ttl, err := s.rdb.TTL(s.ctx, sessionKey).Result()
	require.NoError(s.T(), err, "TTL after Refresh")
	s.AssertTTLWithin(ttl, 1*time.Second, refreshTTL)
}

func (s *GatewayCacheSuite) TestRefreshSessionTTL_MissingKey() {
	// RefreshSessionTTL on a missing key should not error (no-op)
	err := s.cache.RefreshSessionTTL(s.ctx, 1, "missing-session", 1*time.Minute)
	require.NoError(s.T(), err, "RefreshSessionTTL on missing key should not error")
}

func (s *GatewayCacheSuite) TestDeleteSessionAccountID() {
	sessionID := "openai:s4"
	accountID := int64(102)
	groupID := int64(1)
	sessionTTL := 1 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, accountID, sessionTTL), "SetSessionAccountID")
	require.NoError(s.T(), s.cache.DeleteSessionAccountID(s.ctx, groupID, sessionID), "DeleteSessionAccountID")

	_, err := s.cache.GetSessionAccountID(s.ctx, groupID, sessionID)
	require.True(s.T(), errors.Is(err, service.ErrStickySessionNotFound), "expected ErrStickySessionNotFound after delete")
}

func (s *GatewayCacheSuite) TestGetSessionAccountID_CorruptedValue() {
	sessionID := "corrupted"
	groupID := int64(1)
	sessionKey := buildSessionKey(groupID, sessionID)

	// Set a non-integer value
	require.NoError(s.T(), s.rdb.Set(s.ctx, sessionKey, "not-a-number", 1*time.Minute).Err(), "Set invalid value")

	_, err := s.cache.GetSessionAccountID(s.ctx, groupID, sessionID)
	require.Error(s.T(), err, "expected error for corrupted value")
	require.False(s.T(), errors.Is(err, service.ErrStickySessionNotFound), "expected parsing error, not a miss")
}

func TestGatewayCacheSuite(t *testing.T) {
	suite.Run(t, new(GatewayCacheSuite))
}

func (s *GatewayCacheSuite) TestIssue715AgentOwnership() {
	cache := &gatewayCache{rdb: s.rdb}
	other := &gatewayCache{rdb: s.rdb}
	key := strings.Repeat("7", 64)
	require.NoError(s.T(), cache.PublishOpenAIAgentRecovery(s.ctx, key, "route-a", time.Minute))
	value, err := other.GetOpenAIAgentRecovery(s.ctx, key)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "route-a", value)
	require.NoError(s.T(), other.PublishOpenAIAgentRecovery(s.ctx, key, "route-b", 2*time.Minute))
	require.NoError(s.T(), cache.PublishOpenAIAgentRecovery(s.ctx, key, "route-a", time.Second))
	value, err = other.GetOpenAIAgentRecovery(s.ctx, key)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "conflict", value, "real Redis must retain a conflict after further successes")
	redisKey, err := openAIAgentRecoveryKey(key)
	require.NoError(s.T(), err)
	ttl, err := s.rdb.PTTL(s.ctx, redisKey).Result()
	require.NoError(s.T(), err)
	require.Greater(s.T(), ttl, time.Minute, "a shorter refresh must not truncate conflict retention")
	require.NoError(s.T(), s.rdb.Del(s.ctx, redisKey).Err())
	value, err = cache.GetOpenAIAgentRecovery(s.ctx, key)
	require.NoError(s.T(), err)
	require.Empty(s.T(), value)
	pendingKey := strings.Repeat("d", 64)
	require.NoError(s.T(), cache.ReserveOpenAIAgentRecovery(s.ctx, pendingKey, "route-a", "pending:first", time.Minute))
	require.Error(s.T(), other.FinishOpenAIAgentRecovery(s.ctx, pendingKey, "route-a", "pending:other", false, time.Minute))
	require.NoError(s.T(), other.FinishOpenAIAgentRecovery(s.ctx, pendingKey, "route-a", "pending:first", true, time.Minute))
	raw, err := cache.GetOpenAIAgentRecovery(s.ctx, pendingKey)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "route-a", raw)
	require.NoError(s.T(), other.ReserveOpenAIAgentRecovery(s.ctx, pendingKey, "route-a", "pending:next", time.Minute))
	require.NoError(s.T(), other.FinishOpenAIAgentRecovery(s.ctx, pendingKey, "route-a", "pending:next", false, time.Minute))
	raw, err = cache.GetOpenAIAgentRecovery(s.ctx, pendingKey)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "route-a", raw, "abort cannot revoke an existing grant")

}
