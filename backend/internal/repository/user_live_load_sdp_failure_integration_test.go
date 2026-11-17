//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/regional/slots"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type userLiveLoadSDPSnapshot struct {
	service.SchedulerCache
	accounts []*service.Account
}

func (s *userLiveLoadSDPSnapshot) GetSnapshot(context.Context, service.SchedulerBucket) ([]*service.Account, bool, error) {
	return s.accounts, true, nil
}

func (s *userLiveLoadSDPSnapshot) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	for _, account := range s.accounts {
		if account.ID == id {
			return account, nil
		}
	}
	return nil, service.ErrAccountNotFound
}

func (s *userLiveLoadSDPSnapshot) UpdateLastUsed(context.Context, map[int64]time.Time) error {
	return nil
}

type userLiveLoadSDPFailure struct {
	mode     string
	calls    int
	cancel   context.CancelFunc
	snapshot *userLiveLoadSDPSnapshot
}

func (s *userLiveLoadSDPFailure) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.calls++
	if s.mode == "cancel" {
		s.cancel()
		return nil, req.Context().Err()
	}
	if s.mode == "selection-exhausted" {
		s.snapshot.accounts = nil
	}
	status := http.StatusServiceUnavailable
	if s.mode == "nonretryable" {
		status = http.StatusBadRequest
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"offline refusal","type":"invalid_request_error"}}`))}, nil
}

func (s *userLiveLoadSDPFailure) DoWithTLS(req *http.Request, proxy string, account int64, max int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxy, account, max)
}

func TestUserLiveLoadRegionalCreateSDPTerminalFailureClosesJoint(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, userMax := range []int{0, 1} {
			for _, mode := range []string{"nonretryable", "retry-exhausted", "selection-exhausted", "cancel"} {
				t.Run(fmt.Sprintf("local=%t/userMax=%d/%s", local, userMax, mode), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					c := userLiveLoadRegionalCache(t, local)
					id := time.Now().UnixNano()
					concurrency := service.NewConcurrencyService(c)
					if userMax > 0 {
						acquireUserLiveLoadResource(t, c, "user", id, "setup-user")
						require.NoError(t, c.ReleaseUserSlot(ctx, id, "setup-user"))
					}
					user, err := concurrency.AcquireUserSlot(ctx, id, userMax)
					require.NoError(t, err)
					require.True(t, user.Acquired)
					snapshot := &userLiveLoadSDPSnapshot{}
					for i := int64(1); i <= 6; i++ {
						aid := id + i
						acquireUserLiveLoadResource(t, c, "account", aid, "setup-account")
						require.NoError(t, c.ReleaseAccountSlot(ctx, aid, "setup-account"))
						snapshot.accounts = append(snapshot.accounts, &service.Account{
							ID: aid, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
							Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: int(i),
							Credentials: map[string]any{"access_token": "offline-token", "chatgpt_account_id": "offline-account"},
						})
					}
					acquireUserLiveLoadResource(t, c, "api_key", id, "key-source")
					upstream := &userLiveLoadSDPFailure{mode: mode, cancel: cancel, snapshot: snapshot}
					base, ok := c.ConcurrencyCache.(*concurrencyCache)
					require.True(t, ok)
					gateway := service.NewUserLiveLoadGatewayForTest(NewGatewayCache(base.rdb), concurrency,
						service.NewSchedulerSnapshotService(snapshot, nil, nil, nil, nil), upstream)
					created, err := gateway.CreateLiveCall(ctx, &service.LiveCallRequest{SDP: "v=0\r\n", Session: json.RawMessage(`{"model":"gpt-live"}`)},
						service.LiveCallIdentity{UserID: id, APIKeyID: id, UserRequestID: user.RequestID, APIKeyConcurrencyLimit: 1,
							KeyReservation: service.UserLiveLoadKeyReservationForTest(id, "key-source")}, userMax)
					require.Error(t, err)
					require.Nil(t, created)
					expectedCalls := 1
					if mode == "retry-exhausted" {
						expectedCalls = 4
					}
					require.Equal(t, expectedCalls, upstream.calls, "must reach the actual SDP failure branch")
					user.ReleaseFunc()
					var leaseID, state string
					var owner int64
					require.NoError(t, c.database().QueryRow(`SELECT lease_id,state,(identity->>'AccountID')::bigint
FROM regional_live_handoffs WHERE region_id=$1 AND (identity->>'UserID')::bigint=$2`, c.region(), id).Scan(&leaseID, &state, &owner))
					require.Equal(t, "closed", state)
					assertUserLiveLoad(t, c, id, 0)
					n, err := c.count(context.Background(), "api_key", id)
					require.NoError(t, err)
					require.Zero(t, n)
					require.NoError(t, c.ReleaseLiveLease(context.Background(), owner, id, id, leaseID))
					require.ErrorIs(t, c.ReleaseLiveLease(context.Background(), 0, id, id, leaseID), slots.ErrConflict)
					require.ErrorIs(t, c.ReleaseLiveLease(context.Background(), owner+100, id, id, leaseID), slots.ErrConflict)
				})
			}
		}
	}
}
