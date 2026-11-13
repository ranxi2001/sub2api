//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

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
	}{
		{"primary_weekly_zero_neighbor", "0", false},
		{"secondary_weekly_invalid_neighbor", "invalid", true},
		{"primary_weekly_five_hour_neighbor", "300", false},
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

func TestOAuth429WeeklyOnlyHeaderBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers http.Header
		want    bool
	}{
		{"5h_only_100", http.Header{"X-Codex-Primary-Window-Minutes": {"300"}, "X-Codex-Primary-Used-Percent": {"100"}}, false},
		{"weekly_99_5h_0", weekly429Headers("99", "0", false), true},
		{"weekly_0_5h_100", weekly429Headers("0", "100", false), false},
		{"weekly_equal_threshold", weekly429Headers("98", "100", false), false},
		{"unknown_primary_100", http.Header{"X-Codex-Primary-Used-Percent": {"100"}}, false},
		{"unknown_windows", http.Header{"X-Codex-Primary-Window-Minutes": {"60"}, "X-Codex-Primary-Used-Percent": {"100"}}, false},
		{"weekly_missing_usage", weekly429Headers("", "100", false), false},
		{"weekly_invalid_usage", weekly429Headers("invalid", "100", false), false},
		{"weekly_nan", weekly429Headers("NaN", "100", false), false},
		{"weekly_infinity", weekly429Headers("+Inf", "100", false), false},
		{"weekly_negative", weekly429Headers("-1", "100", false), false},
		{"weekly_over_100", weekly429Headers("101", "100", false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cacheUsed := 99.0
			if tc.name == "5h_only_100" || tc.name == "unknown_primary_100" || tc.name == "unknown_windows" {
				cacheUsed = 20
			}
			assertWeeklyOnly429Write(t, tc.headers, tc.want, cacheUsed)
		})
	}
	for _, reverse := range []bool{false, true} {
		for _, neighbor := range []string{"0", "-1", "invalid", "", "absent", "300", "60"} {
			t.Run(fmt.Sprintf("weekly_reverse_%t_neighbor_%s", reverse, neighbor), func(t *testing.T) {
				h := weekly429Headers("99", "100", reverse)
				short := "secondary"
				if reverse {
					short = "primary"
				}
				if neighbor == "absent" {
					for _, field := range []string{"window-minutes", "used-percent", "reset-after-seconds"} {
						h.Del("x-codex-" + short + "-" + field)
					}
				} else {
					h.Set("x-codex-"+short+"-window-minutes", neighbor)
					h.Set("x-codex-"+short+"-reset-after-seconds", "invalid")
				}
				assertWeeklyOnly429Write(t, h, true)
			})
		}
	}
	for _, tc := range []struct {
		name, key string
		values    []string
	}{
		{"two_weekly_windows", "x-codex-secondary-window-minutes", []string{"10080"}},
		{"duplicate_weekly_window", "x-codex-primary-window-minutes", []string{"10080", "10080"}},
		{"conflicting_weekly_window", "x-codex-primary-window-minutes", []string{"10080", "300"}},
		{"hidden_weekly_neighbor", "x-codex-secondary-window-minutes", []string{"300", "10080"}},
		{"comma_weekly_neighbor", "x-codex-secondary-window-minutes", []string{"300, 10080"}},
		{"duplicate_weekly_usage", "x-codex-primary-used-percent", []string{"99", "99"}},
		{"conflicting_weekly_usage", "x-codex-primary-used-percent", []string{"99", "0"}},
		{"empty_weekly_usage", "x-codex-primary-used-percent", nil},
		{"duplicate_weekly_reset", "x-codex-primary-reset-after-seconds", []string{"7200", "7200"}},
		{"invalid_weekly_reset", "x-codex-primary-reset-after-seconds", []string{"invalid"}},
		{"empty_weekly_reset", "x-codex-primary-reset-after-seconds", nil},
		{"elapsed_weekly_reset", "x-codex-primary-reset-after-seconds", []string{"0"}},
		{"oversized_weekly_reset", "x-codex-primary-reset-after-seconds", []string{"604801"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := weekly429Headers("99", "0", false)
			h[http.CanonicalHeaderKey(tc.key)] = tc.values
			assertWeeklyOnly429Write(t, h, false)
		})
	}
}

func assertWeeklyOnly429Write(t *testing.T, headers http.Header, want bool, cachedUsage ...float64) {
	t.Helper()
	// A high cached value deliberately challenges the no-fallback boundary.
	account := scheduling429Account()
	account.Extra["codex_7d_window_minutes"] = 10080
	if len(cachedUsage) > 0 {
		account.Extra["codex_7d_used_percent"] = cachedUsage[0]
	}
	repo := &weeklyOnly429Repo{}
	rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rates.HandleUpstreamError(t.Context(), account, 429, headers, nil)
	if want {
		require.Equal(t, []bool{false}, repo.values, "only explicit valid weekly evidence may permanently disable")
	} else {
		require.Empty(t, repo.ids, "ambiguous, missing or low weekly evidence must not reuse cached high usage")
	}
}

func TestOAuth429WeeklyOnlyCachedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Account)
		want   bool
	}{
		{"explicit_fresh_weekly", func(*Account) {}, true},
		{"missing_window_identity", func(a *Account) { delete(a.Extra, "codex_7d_window_minutes") }, false},
		{"invalid_window_identity", func(a *Account) { a.Extra["codex_7d_window_minutes"] = "invalid" }, false},
		{"five_hour_identity", func(a *Account) { a.Extra["codex_7d_window_minutes"] = 300 }, false},
		{"stale_observation", func(a *Account) {
			a.Extra["codex_usage_updated_at"] = time.Now().Add(-3 * time.Hour).Format(time.RFC3339Nano)
		}, false},
		{"future_observation", func(a *Account) {
			a.Extra["codex_usage_updated_at"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
		}, false},
		{"missing_observation", func(a *Account) { delete(a.Extra, "codex_usage_updated_at") }, false},
		{"identity_conflict", func(a *Account) {
			a.Credentials = map[string]any{"chatgpt_account_id": "fictional-owner"}
			a.Extra["chatgpt_account_id"] = "fictional-other"
		}, false},
		{"workspace_conflict", func(a *Account) {
			a.Credentials = map[string]any{"workspace_id": "fictional-owner"}
			a.Extra["workspace_id"] = "fictional-other"
		}, false},
		{"reset_elapsed", func(a *Account) { a.Extra["codex_7d_reset_at"] = time.Now().Add(-time.Minute).Format(time.RFC3339Nano) }, false},
		{"reset_invalid", func(a *Account) { a.Extra["codex_7d_reset_at"] = "invalid" }, false},
		{"reset_future", func(a *Account) { a.Extra["codex_7d_reset_at"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano) }, true},
		{"relative_reset_elapsed", func(a *Account) { a.Extra["codex_7d_reset_after_seconds"] = 1 }, false},
		{"relative_reset_future", func(a *Account) { a.Extra["codex_7d_reset_after_seconds"] = 7200 }, true},
		{"fresh_field_versions", func(a *Account) {
			a.Extra[CodexUsageFieldVersionsKey] = weeklyOnlyVersions(time.Now().Add(-time.Minute))
		}, true},
		{"stale_usage_fresh_global", func(a *Account) {
			v := weeklyOnlyVersions(time.Now())
			v["codex_7d_used_percent"] = strconv.FormatInt(time.Now().Add(-3*time.Hour).UnixNano(), 10)
			a.Extra[CodexUsageFieldVersionsKey] = v
		}, false},
		{"stale_window_fresh_usage", func(a *Account) {
			v := weeklyOnlyVersions(time.Now())
			v["codex_7d_window_minutes"] = strconv.FormatInt(time.Now().Add(-3*time.Hour).UnixNano(), 10)
			a.Extra[CodexUsageFieldVersionsKey] = v
		}, false},
		{"unversioned_window", func(a *Account) {
			v := weeklyOnlyVersions(time.Now())
			delete(v, "codex_7d_window_minutes")
			a.Extra[CodexUsageFieldVersionsKey] = v
		}, false},
		{"invalid_versions", func(a *Account) { a.Extra[CodexUsageFieldVersionsKey] = "invalid" }, false},
		{"missing_relative_reset_version", func(a *Account) {
			a.Extra[CodexUsageFieldVersionsKey] = weeklyOnlyVersions(time.Now())
			a.Extra["codex_7d_reset_after_seconds"] = 7200
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := scheduling429Account()
			a.Extra["codex_7d_window_minutes"] = 10080
			tc.mutate(a)
			r := &weeklyOnly429Repo{}
			s := NewRateLimitService(r, nil, &config.Config{}, nil, nil)
			s.HandleUpstreamError(t.Context(), a, 429, nil, nil)
			require.Equal(t, tc.want, len(r.ids) > 0, "cached weekly identity and original observation must be trustworthy")
		})
	}
}

func weeklyOnlyVersions(observed time.Time) map[string]any {
	stamp := strconv.FormatInt(observed.UnixNano(), 10)
	return map[string]any{"codex_7d_used_percent": stamp, "codex_7d_window_minutes": stamp}
}

func TestOAuth429WeeklyOnlyPolicyGates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		mutate func(*Account)
		want   bool
	}{
		{"own_lower_threshold", 429, func(a *Account) { a.Extra[OpenAIDisableSchedulingOn4297dThresholdExtraKey] = 97.5 }, true},
		{"own_equality", 429, func(a *Account) { a.Extra[OpenAIDisableSchedulingOn4297dThresholdExtraKey] = 99.0 }, false},
		{"own_higher_threshold", 429, func(a *Account) { a.Extra[OpenAIDisableSchedulingOn4297dThresholdExtraKey] = 100.0 }, false},
		{"invalid_threshold", 429, func(a *Account) { a.Extra[OpenAIDisableSchedulingOn4297dThresholdExtraKey] = "invalid" }, false},
		{"disabled_feature", 429, func(a *Account) { a.Extra[OpenAIDisableSchedulingOn429ExtraKey] = false }, false},
		{"not_upstream_429", 503, func(*Account) {}, false},
		{"body_only_weekly_limit", 429, func(a *Account) { delete(a.Extra, "codex_7d_used_percent") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := scheduling429Account()
			tc.mutate(a)
			h := weekly429Headers("99", "0", false)
			if tc.name == "body_only_weekly_limit" {
				h = nil
			}
			r := &weeklyOnly429Repo{}
			s := NewRateLimitService(r, nil, &config.Config{}, nil, nil)
			s.HandleUpstreamError(t.Context(), a, tc.status, h, []byte(`{"error":{"type":"usage_limit_reached","limit_window_minutes":10080}}`))
			require.Equal(t, tc.want, len(r.ids) > 0)
		})
	}
}

func TestOAuth429WeeklyOnlyCombinedRotation(t *testing.T) {
	for _, initial := range []bool{false, true} {
		t.Run(fmt.Sprintf("initial_schedulable_%t", initial), func(t *testing.T) {
			rotation, rotationRepo, account := newTOTP429TestService(t)
			account.Schedulable = initial
			account.Extra[OpenAIDisableSchedulingOn429ExtraKey] = true
			repo := &weeklyOnly429Repo{}
			rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			rates.openAITOTP429Rotator = rotation
			h := weekly429Headers("99", "0", false)
			h.Set("x-codex-secondary-window-minutes", "0")
			rates.HandleUpstreamError(t.Context(), account, 429, h, nil)
			require.Equal(t, []bool{false}, repo.values, "both enabled policies must only persist a disable")
			require.Equal(t, 1, rotationRepo.calls, "real automatic rotation service must still queue the task")
			require.Equal(t, initial, account.Schedulable, "neither policy may rewrite the shared/manual snapshot")
		})
	}
}

func TestOAuth429WeeklyOnlyCacheDoesNotRelabelUnidentifiedUsage(t *testing.T) {
	for _, order := range []string{"usage_without_window", "window_without_usage"} {
		t.Run(order, func(t *testing.T) {
			now := time.Now()
			first := weekly429Headers("20", "0", false)
			second := http.Header{"X-Codex-Primary-Used-Percent": {"100"}}
			if order == "window_without_usage" {
				first = http.Header{"X-Codex-Primary-Used-Percent": {"100"}}
				second = http.Header{"X-Codex-Primary-Window-Minutes": {"10080"}}
			}
			// Use the real parser, normalizer and field-version merge: a partial
			// packet must not borrow another packet's weekly identity.
			oldSnapshot, newSnapshot := ParseCodexRateLimitHeaders(first), ParseCodexRateLimitHeaders(second)
			oldSnapshot.UpdatedAt = now.Add(-2 * time.Minute).Format(time.RFC3339Nano)
			newSnapshot.UpdatedAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
			older := buildCodexUsageExtraUpdates(oldSnapshot, now)
			newer := buildCodexUsageExtraUpdates(newSnapshot, now)
			a := scheduling429Account()
			a.Extra = mergeCodexUsageUpdates(older, newer)
			a.Extra[OpenAIDisableSchedulingOn429ExtraKey] = true
			repo := &weeklyOnly429Repo{}
			rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			rates.HandleUpstreamError(t.Context(), a, 429, nil, nil)
			require.Empty(t, repo.ids, "cached usage must share the explicit weekly identity's source observation")
		})
	}
}

func TestOAuth429WeeklyOnlyNonweeklyHeadersAllowExplicitCache(t *testing.T) {
	for _, tc := range []struct {
		name, neighbor string
		mutate         func(*Account)
		want           bool
	}{
		{"five_hour_100_zero_neighbor", "0", nil, true},
		{"five_hour_100_invalid_neighbor", "invalid", nil, true},
		{"missing_cached_usage", "0", func(a *Account) { delete(a.Extra, "codex_7d_used_percent") }, false},
		{"unidentified_cached_usage", "0", func(a *Account) { delete(a.Extra, "codex_7d_window_minutes") }, false},
		{"cached_usage_equal_own_threshold", "0", func(a *Account) { a.Extra[OpenAIDisableSchedulingOn4297dThresholdExtraKey] = 99.0 }, false},
		{"cached_usage_below_threshold", "0", func(a *Account) { a.Extra["codex_7d_used_percent"] = 20.0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := scheduling429Account()
			a.Extra[CodexUsageFieldVersionsKey] = weeklyOnlyVersions(time.Now().Add(-time.Minute))
			if tc.mutate != nil {
				tc.mutate(a)
			}
			h := http.Header{}
			h.Set("x-codex-primary-window-minutes", "300")
			h.Set("x-codex-primary-used-percent", "100")
			h.Set("x-codex-secondary-window-minutes", tc.neighbor)
			h.Set("x-codex-secondary-used-percent", "0")
			repo := &weeklyOnly429Repo{}
			rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			rates.HandleUpstreamError(t.Context(), a, 429, h, nil)
			require.Equal(t, tc.want, len(repo.ids) > 0, "nonweekly headers must not veto fresh explicit weekly cache")
			if tc.want {
				require.Equal(t, []bool{false}, repo.values)
			}
			require.NotEmpty(t, repo.extra, "real quota parser/persistence path must run")
		})
	}
}

type weeklyOnlyPersistedRepo struct {
	weeklyOnly429Repo
	stored *Account
}

func (r *weeklyOnlyPersistedRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	if _, err := json.Marshal(updates); err != nil {
		return err
	}
	r.stored.Extra = mergeCodexUsageUpdates(r.stored.Extra, updates)
	return nil
}

func (r *weeklyOnlyPersistedRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	reloaded := *r.stored
	reloaded.Extra = shallowCopyMap(r.stored.Extra)
	return &reloaded, nil
}

func TestOAuth429WeeklyOnlySequentialCache(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		values    []string
	}{
		{"duplicate_usage", "x-codex-primary-used-percent", []string{"99", "20"}},
		{"conflicting_window", "x-codex-primary-window-minutes", []string{"10080", "300"}},
		{"conflicting_weekly_windows", "x-codex-secondary-window-minutes", []string{"10080"}},
		{"invalid_weekly_usage", "x-codex-primary-used-percent", []string{"invalid"}},
		{"nan_weekly_usage", "x-codex-primary-used-percent", []string{"NaN"}},
		{"infinite_weekly_usage", "x-codex-primary-used-percent", []string{"+Inf"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := scheduling429Account()
			initial := ParseCodexRateLimitHeaders(weekly429Headers("99", "0", false))
			initial.UpdatedAt = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
			a.Extra = mergeCodexUsageUpdates(a.Extra, buildCodexUsageExtraUpdates(initial, time.Now()))
			repo := &weeklyOnlyPersistedRepo{stored: a}
			rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			h := weekly429Headers("99", "0", false)
			h[http.CanonicalHeaderKey(tc.key)] = tc.values
			first, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			rates.HandleUpstreamError(t.Context(), first, 429, h, nil)
			require.Empty(t, repo.ids, "invalid weekly headers must not disable on the first request")
			reloaded, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			rates.HandleUpstreamError(t.Context(), reloaded, 429, nil, nil)
			require.Empty(t, repo.ids, "rejected weekly headers must not poison a later headerless 429 cache")
			// A later independently valid weekly observation restores usable evidence.
			valid := ParseCodexRateLimitHeaders(weekly429Headers("99", "0", false))
			require.NoError(t, repo.UpdateExtra(t.Context(), a.ID, buildCodexUsageExtraUpdates(valid, time.Now())))
			reloaded, err = repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			rates.HandleUpstreamError(t.Context(), reloaded, 429, nil, nil)
			require.Equal(t, []bool{false}, repo.values)
		})
	}
}

func TestOAuth429WeeklyOnlyNonweeklyPersistenceRetainsEvidence(t *testing.T) {
	a := scheduling429Account()
	weekly := ParseCodexRateLimitHeaders(weekly429Headers("99", "0", false))
	weekly.UpdatedAt = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
	a.Extra = mergeCodexUsageUpdates(a.Extra, buildCodexUsageExtraUpdates(weekly, time.Now()))
	repo := &weeklyOnlyPersistedRepo{stored: a}
	nonweekly := http.Header{}
	nonweekly.Set("x-codex-primary-window-minutes", "300")
	nonweekly.Set("x-codex-primary-used-percent", "100")
	nonweekly.Set("x-codex-secondary-window-minutes", "0")
	nonweekly.Set("x-codex-secondary-used-percent", "0")
	require.NoError(t, repo.UpdateExtra(t.Context(), a.ID, buildCodexUsageExtraUpdates(ParseCodexRateLimitHeaders(nonweekly), time.Now())))
	reloaded, err := repo.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rates.HandleUpstreamError(t.Context(), reloaded, 429, nil, nil)
	require.Equal(t, []bool{false}, repo.values, "nonweekly persistence must retain independently valid weekly evidence")
}
