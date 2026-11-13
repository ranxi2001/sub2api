//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type weeklyOnly429Repo struct {
	scheduling429Repo
	extra map[string]any
}

func (r *weeklyOnly429Repo) UpdateExtra(_ context.Context, _ int64, extra map[string]any) error {
	r.extra = extra
	return nil
}

// Exercise the public error service, its durable scheduling write and the
// existing Codex header parser/persistence path, using fictional observations.
func TestOAuth429WeeklyOnlyService(t *testing.T) {
	for _, tc := range []struct {
		name, neighbor string
		reverse        bool
		want           bool
	}{
		{"primary_weekly_zero_neighbor", "0", false, true},
		{"secondary_weekly_invalid_neighbor", "invalid", true, true},
		{"primary_weekly_five_hour_neighbor", "300", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := scheduling429Account()
			delete(account.Extra, "codex_7d_used_percent")
			headers := weekly429Headers("99", "0", tc.reverse)
			neighbor := "secondary"
			if tc.reverse {
				neighbor = "primary"
			}
			headers.Set("x-codex-"+neighbor+"-window-minutes", tc.neighbor)
			repo := &weeklyOnly429Repo{}
			rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			rates.HandleUpstreamError(t.Context(), account, http.StatusTooManyRequests, headers, nil)
			require.Equal(t, []int64{account.ID}, repo.ids, "explicit weekly 99 must disable despite an unrelated neighbor")
			require.Equal(t, []bool{false}, repo.values)
			require.NotEmpty(t, repo.extra, "the existing quota header parser must still persist the response")
		})
	}
}
