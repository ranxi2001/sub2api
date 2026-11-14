package qqbot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func groupInfoTestService(t *testing.T, handle http.HandlerFunc) (*Service, Config) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
			return
		}
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "QQBot test-token" || r.Header.Get("X-Union-Appid") == "" {
			t.Error("group metadata must use an authenticated GET")
		}
		handle(w, r)
	}))
	t.Cleanup(server.Close)
	s := New(nil, nil, false, nil)
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	return s, Config{AppID: "app", AppSecret: "secret"}
}

func TestQQGroupInfoCachesNamesAndIsolatesCredentials(t *testing.T) {
	var calls atomic.Int32
	s, cfg := groupInfoTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v2/groups/group/info" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"group_openid":"group","group_name":"  交流群  "}`))
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info := s.loadGroupInfo(context.Background(), cfg, "group")
			if info.Name != "交流群" {
				t.Errorf("unexpected name: %s", info.Name)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
	for _, changed := range []Config{
		{AppID: "other", AppSecret: cfg.AppSecret},
		{AppID: cfg.AppID, AppSecret: "new-secret"},
		{AppID: cfg.AppID, AppSecret: cfg.AppSecret, Sandbox: true},
	} {
		info := s.loadGroupInfo(context.Background(), changed, "group")
		require.Equal(t, "available", info.Status)
		require.Equal(t, changed.Sandbox, info.Sandbox)
	}
	require.EqualValues(t, 4, calls.Load())
}

func TestQQGroupInfoPermissionDenialStopsApplicationFanout(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			s, cfg := groupInfoTestService(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"code":11253,"message":"private upstream details"}`))
			})
			for _, group := range []string{"first", "second"} {
				require.Equal(t, "permission_required", s.loadGroupInfo(context.Background(), cfg, group).Status)
			}
			require.Equal(t, 1, calls)
			cfg.AppSecret = "updated"
			require.Equal(t, "permission_required", s.loadGroupInfo(context.Background(), cfg, "second").Status)
			require.Equal(t, 2, calls)
		})
	}
}

func TestQQGroupInfoRejectsInvalidResponsesAndCachesFailures(t *testing.T) {
	for _, body := range []string{`not-json`, `{"group_name":" "}`, `{"group_openid":"other","group_name":"wrong group"}`, `{"code":500,"group_name":"bad"}`} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			s, cfg := groupInfoTestService(t, func(w http.ResponseWriter, _ *http.Request) { calls++; _, _ = w.Write([]byte(body)) })
			for i := 0; i < 2; i++ {
				info := s.loadGroupInfo(context.Background(), cfg, "group")
				require.Empty(t, info.Name)
				require.Equal(t, "unavailable", info.Status)
			}
			require.Equal(t, 1, calls)
		})
	}
}

func TestQQGroupInfoHonorsRateLimitAndCancellation(t *testing.T) {
	s, cfg := groupInfoTestService(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"group_name":"Test"}`)) })
	for i := 0; i < 30; i++ {
		require.Equal(t, "available", s.loadGroupInfo(context.Background(), cfg, fmt.Sprint(i)).Status)
	}
	info := s.loadGroupInfo(context.Background(), cfg, "overflow")
	require.Equal(t, "rate_limited", info.Status)
	require.InDelta(t, 60, info.RetryAfterSeconds, 2)
	require.Equal(t, "available", s.loadGroupInfo(context.Background(), cfg, "0").Status)
	for i := range s.groupInfo.requests {
		s.groupInfo.requests[i] = time.Now().Add(-2 * time.Minute)
	}
	require.Equal(t, "available", s.loadGroupInfo(context.Background(), cfg, "overflow").Status)
	s.groupInfo.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Equal(t, "unavailable", s.loadGroupInfo(ctx, cfg, "canceled").Status)
	<-s.groupInfo.gate
}

func TestQQGroupInfoUpstreamRateLimitPausesOtherGroups(t *testing.T) {
	calls := 0
	s, cfg := groupInfoTestService(t, func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusTooManyRequests) })
	for _, group := range []string{"first", "second"} {
		require.Equal(t, "rate_limited", s.loadGroupInfo(context.Background(), cfg, group).Status)
	}
	require.Equal(t, 1, calls)
}
