package service

import (
	"context"
	"testing"
	"time"

	gocache "github.com/patrickmn/go-cache"
)

var rateFallbackSink float64

func TestUnconfiguredRateFallbackAvoidsTemporaryCache(t *testing.T) {
	ctx := context.Background()
	gateway := &GatewayService{}
	openai := &OpenAIGatewayService{}
	for _, tc := range []struct {
		name string
		fn   func() float64
	}{
		{"gateway", func() float64 { return gateway.getUserGroupRateMultiplier(ctx, 7, 9, 1.25) }},
		{"openai", func() float64 { return openai.ResolveUserGroupRateMultiplier(ctx, 7, 9, 1.25) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fn(); got != 1.25 {
				t.Fatalf("fallback=%v, want 1.25", got)
			}
			if allocs := testing.AllocsPerRun(25, func() { rateFallbackSink = tc.fn() }); allocs != 0 {
				t.Fatalf("unconfigured fallback allocated %v objects per call", allocs)
			}
		})
	}
}

func TestFallbackStillUsesAnExistingRateCache(t *testing.T) {
	cache := gocache.New(time.Minute, 0)
	cache.Set("7:9", 0.75, gocache.DefaultExpiration)
	gateway := &GatewayService{userGroupRateCache: cache}
	if got := gateway.getUserGroupRateMultiplier(context.Background(), 7, 9, 1.25); got != 0.75 {
		t.Fatalf("cached rate=%v, want 0.75", got)
	}
	openai := &OpenAIGatewayService{userGroupRateResolver: newUserGroupRateResolver(nil, cache, time.Minute, nil, "service.test")}
	if got := openai.ResolveUserGroupRateMultiplier(context.Background(), 7, 9, 1.25); got != 0.75 {
		t.Fatalf("OpenAI cached rate=%v, want 0.75", got)
	}
}

func TestFallbackStillUsesAnExistingRateRepository(t *testing.T) {
	rate := 0.75
	repo := &userGroupRateResolverRepoStub{rate: &rate}
	gateway := &GatewayService{userGroupRateRepo: repo}
	if got := gateway.getUserGroupRateMultiplier(context.Background(), 7, 9, 1.25); got != rate {
		t.Fatalf("repository rate=%v, want %v", got, rate)
	}
	if repo.calls != 1 {
		t.Fatalf("repository calls=%d, want 1", repo.calls)
	}
}
