//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIssue765ImageAuthorizationRedis(t *testing.T) {
	for _, tc := range issue765ImageCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			rdb := testRedis(t)
			writer := NewSchedulerCache(rdb)
			bucket := service.SchedulerBucket{GroupID: 765, Platform: tc.account.Platform, Mode: service.SchedulerModeSingle}
			token, err := writer.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, writer.SetSnapshot(ctx, bucket, token, []service.Account{tc.account}))
			// A separate reader must decode Redis metadata, not reuse a writer's objects.
			reader := NewSchedulerCache(rdb)
			check := func(want issue765ImageCase) {
				t.Helper()
				full, err := reader.GetAccount(ctx, tc.account.ID)
				require.NoError(t, err)
				issue765CheckImageAuthorization(t, want, full, "full")
				candidates, hit, err := reader.GetSnapshot(ctx, bucket)
				require.NoError(t, err)
				require.True(t, hit)
				require.Len(t, candidates, 1)
				issue765CheckImageAuthorization(t, want, candidates[0], "metadata")
			}
			check(tc)
			if tc.allowed {
				// Saving grants and revoking them must refresh a memoized candidate
				// without a membership rebuild, including explicitly empty lists.
				for _, update := range []struct {
					extra   map[string]any
					allowed bool
				}{
					{map[string]any{service.ExtraImageModelsKey: []string{}, service.ExtraImageModelPrefixesKey: []string{}}, false},
					{tc.account.Extra, true},
					{nil, false},
				} {
					updated := tc.account
					updated.Extra = update.extra
					require.NoError(t, writer.SetAccount(ctx, &updated))
					want := tc
					want.allowed = update.allowed
					check(want)
				}
			}
		})
	}
}
