//go:build unit

package service

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func weekly429Headers(weekly, fiveHour string, reverse bool) http.Header {
	headers := http.Header{}
	week, short := "primary", "secondary"
	if reverse {
		week, short = short, week
	}
	headers.Set("x-codex-"+week+"-window-minutes", "10080")
	headers.Set("x-codex-"+short+"-window-minutes", "300")
	if weekly != "" {
		headers.Set("x-codex-"+week+"-used-percent", weekly)
	}
	if fiveHour != "" {
		headers.Set("x-codex-"+short+"-used-percent", fiveHour)
	}
	return headers
}

func TestDisableScheduling429RequiresWeeklyUsageStrictlyAboveThreshold(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*Account)
		headers http.Header
		want    bool
	}{
		{name: "legacy account defaults to 98", want: true},
		{name: "exactly 98 does not disable", mutate: func(a *Account) { a.Extra["codex_7d_used_percent"] = 98.0 }},
		{name: "fraction above default", mutate: func(a *Account) { a.Extra["codex_7d_used_percent"] = 98.01 }, want: true},
		{name: "custom fractional threshold", mutate: func(a *Account) { a.Extra[OpenAIDisableSchedulingOn4297dThresholdExtraKey] = 99.5 }},
		{name: "custom lower threshold", mutate: func(a *Account) { a.Extra[OpenAIDisableSchedulingOn4297dThresholdExtraKey] = 97.5 }, want: true},
		{name: "zero is a valid configured threshold", mutate: func(a *Account) { a.Extra[OpenAIDisableSchedulingOn4297dThresholdExtraKey] = 0.0 }, want: true},
		{name: "100 never exceeded by valid usage", mutate: func(a *Account) { a.Extra[OpenAIDisableSchedulingOn4297dThresholdExtraKey] = 100.0 }, headers: weekly429Headers("100", "100", false)},
		{name: "five hour exhaustion alone", mutate: func(a *Account) { a.Extra["codex_7d_used_percent"] = 20.0; a.Extra["codex_5h_used_percent"] = 100.0 }},
		{name: "missing weekly usage", mutate: func(a *Account) { delete(a.Extra, "codex_7d_used_percent"); a.Extra["codex_5h_used_percent"] = 100.0 }},
		{name: "cached short window mislabeled as weekly", mutate: func(a *Account) { a.Extra["codex_7d_window_minutes"] = 300 }},
		{name: "missing timestamp", mutate: func(a *Account) { delete(a.Extra, "codex_usage_updated_at") }},
		{name: "invalid timestamp", mutate: func(a *Account) { a.Extra["codex_usage_updated_at"] = "invalid" }},
		{name: "future observation", mutate: func(a *Account) {
			a.Extra["codex_usage_updated_at"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
		}},
		{name: "stale snapshot", mutate: func(a *Account) {
			a.Extra["codex_usage_updated_at"] = time.Now().Add(-3 * time.Hour).Format(time.RFC3339Nano)
		}},
		{name: "stale weekly field with fresh five hour update", mutate: func(a *Account) {
			a.Extra[CodexUsageFieldVersionsKey] = map[string]any{"codex_7d_used_percent": strconv.FormatInt(time.Now().Add(-3*time.Hour).UnixNano(), 10)}
		}},
		{name: "unknown weekly field version", mutate: func(a *Account) { a.Extra[CodexUsageFieldVersionsKey] = map[string]any{} }},
		{name: "already reset", mutate: func(a *Account) { a.Extra["codex_7d_reset_at"] = time.Now().Add(-time.Minute).Format(time.RFC3339Nano) }},
		{name: "invalid reset", mutate: func(a *Account) { a.Extra["codex_7d_reset_at"] = "invalid" }},
		{name: "relative reset already passed", mutate: func(a *Account) { a.Extra["codex_7d_reset_after_seconds"] = 1 }},
		{name: "identity mismatch", mutate: func(a *Account) {
			a.Credentials = map[string]any{"chatgpt_account_id": "owner"}
			a.Extra["chatgpt_account_id"] = "other"
		}},
		{name: "new low header overrides old high snapshot", headers: weekly429Headers("20", "100", false)},
		{name: "new high header overrides old low snapshot", mutate: func(a *Account) { a.Extra["codex_7d_used_percent"] = 20.0 }, headers: weekly429Headers("99", "20", false), want: true},
		{name: "latest equality overrides old high", headers: weekly429Headers("98", "100", false)},
		{name: "reversed windows do not confuse five hour quota", headers: weekly429Headers("20", "100", true)},
		{name: "reversed weekly quota", headers: weekly429Headers("99", "20", true), want: true},
		{name: "five hour header alone cannot confirm cached weekly exhaustion", headers: weekly429Headers("", "100", false)},
		{name: "new header validates otherwise stale snapshot", mutate: func(a *Account) { a.Extra["codex_usage_updated_at"] = "invalid" }, headers: weekly429Headers("99", "20", false), want: true},
		{name: "invalid current weekly header blocks fallback", headers: weekly429Headers("invalid", "100", false)},
		{name: "negative current weekly usage", headers: weekly429Headers("-1", "100", false)},
		{name: "over 100 current weekly usage", headers: weekly429Headers("101", "100", false)},
		{name: "NaN current weekly usage", headers: weekly429Headers("NaN", "100", false)},
		{name: "infinite current weekly usage", headers: weekly429Headers("+Inf", "100", false)},
		{name: "current reset already elapsed", headers: func() http.Header {
			h := weekly429Headers("99", "100", false)
			h.Set("x-codex-primary-reset-after-seconds", "0")
			return h
		}()},
		{name: "empty current reset", headers: func() http.Header {
			h := weekly429Headers("99", "100", false)
			h.Set("x-codex-primary-reset-after-seconds", "")
			return h
		}()},
		{name: "reset-only response cannot reuse cached high usage", headers: func() http.Header { h := http.Header{}; h.Set("x-codex-primary-reset-after-seconds", "0"); return h }()},
		{name: "weekly reset and window without usage cannot reuse cached high", headers: func() http.Header {
			h := http.Header{}
			h.Set("x-codex-primary-reset-after-seconds", "0")
			h.Set("x-codex-primary-window-minutes", "10080")
			return h
		}()},
		{name: "weekly window alone cannot reuse cached high", headers: func() http.Header { h := http.Header{}; h.Set("x-codex-primary-window-minutes", "10080"); return h }()},
		{name: "short window alone cannot reuse cached high", headers: func() http.Header { h := http.Header{}; h.Set("x-codex-primary-window-minutes", "300"); return h }()},
		{name: "empty window cannot reuse cached high", headers: func() http.Header { h := http.Header{}; h.Set("x-codex-primary-window-minutes", ""); return h }()},
		{name: "empty weekly window invalidates current high usage", headers: func() http.Header {
			h := weekly429Headers("99", "100", false)
			h.Set("x-codex-primary-window-minutes", "")
			return h
		}()},
		{name: "nil header values still invalidate observation", headers: http.Header{http.CanonicalHeaderKey("x-codex-primary-window-minutes"): nil}},
		{name: "invalid current reset", headers: func() http.Header {
			h := weekly429Headers("99", "100", false)
			h.Set("x-codex-primary-reset-after-seconds", "invalid")
			return h
		}()},
		{name: "two short windows cannot invent weekly quota", headers: func() http.Header {
			h := weekly429Headers("99", "100", false)
			h.Set("x-codex-primary-window-minutes", "60")
			return h
		}()},
		{name: "legacy unclassified primary may be five hour", headers: func() http.Header { h := http.Header{}; h.Set("x-codex-primary-used-percent", "100"); return h }()},
		{name: "two weekly windows are ambiguous", headers: func() http.Header {
			h := weekly429Headers("99", "100", false)
			h.Set("x-codex-secondary-window-minutes", "10080")
			return h
		}()},
		{name: "empty current weekly header", headers: func() http.Header {
			h := weekly429Headers("99", "100", false)
			h.Set("x-codex-primary-used-percent", "")
			return h
		}()},
		{name: "invalid current window", headers: func() http.Header {
			h := weekly429Headers("99", "100", false)
			h.Set("x-codex-primary-window-minutes", "invalid")
			return h
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := scheduling429Account()
			if tc.mutate != nil {
				tc.mutate(account)
			}
			repo, blocker := &scheduling429Repo{}, &runtimeBlockRecorder{}
			rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			rates.SetAccountRuntimeBlocker(blocker)
			ctx, disabled := rates.maybeDisableOpenAISchedulingOn429(t.Context(), account, http.StatusTooManyRequests, tc.headers)
			_, repeated := rates.maybeDisableOpenAISchedulingOn429(ctx, account, http.StatusTooManyRequests, tc.headers)
			require.Equal(t, tc.want, disabled)
			require.Equal(t, disabled, repeated)
			if tc.want {
				require.Equal(t, []int64{account.ID}, repo.ids)
				require.Equal(t, []bool{false}, repo.values)
			} else {
				require.Empty(t, repo.ids)
				require.Empty(t, blocker.accounts)
			}
		})
	}
}

func TestDisableScheduling429RejectsInvalidStoredThresholdsAndUsage(t *testing.T) {
	for _, key := range []string{OpenAIDisableSchedulingOn4297dThresholdExtraKey, "codex_7d_used_percent"} {
		for _, invalid := range []any{nil, "invalid", -1.0, 101.0, math.NaN(), math.Inf(1), true} {
			account := scheduling429Account()
			account.Extra[key] = invalid
			repo := &scheduling429Repo{}
			rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			_, disabled := rates.maybeDisableOpenAISchedulingOn429(t.Context(), account, 429, nil)
			require.False(t, disabled, "key=%s invalid=%v", key, invalid)
			require.Empty(t, repo.ids)
		}
	}
}

func TestManaged429WeeklyPolicyUsesOwnerThresholdAndSnapshot(t *testing.T) {
	for _, used := range []float64{20, 99} {
		owner := scheduling429Account()
		owner.Extra["codex_7d_used_percent"] = used
		projection := scheduling429Account()
		projection.AuthConnectionID = "connection"
		projection.Extra["openai_connections_managed"] = true
		projection.Extra[OpenAIDisableSchedulingOn4297dThresholdExtraKey] = 0.0
		repo := &scheduling429Repo{owner: owner}
		rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		_, disabled := rates.maybeDisableOpenAISchedulingOn429(t.Context(), projection, 429, nil)
		require.Equal(t, used > 98, disabled)
		require.Equal(t, []int64{owner.ID}, repo.reads)
	}
}

func TestWeekly429ThresholdConfigValidation(t *testing.T) {
	for _, threshold := range []float64{0, 98, 98.5, 100} {
		cfg := DefaultOAuthAutoConfig()
		cfg.OpenAIDisableSchedulingOn4297dThresholdPercent = threshold
		require.NoError(t, ValidateOAuthAutoConfig(cfg))
	}
	for _, threshold := range []float64{-1, 100.01, math.NaN(), math.Inf(1)} {
		cfg := DefaultOAuthAutoConfig()
		cfg.OpenAIDisableSchedulingOn4297dThresholdPercent = threshold
		require.Error(t, ValidateOAuthAutoConfig(cfg))
	}
	for _, raw := range []string{`{"openai_disable_scheduling_on_429_7d_threshold_percent":null}`, `{"openai_disable_scheduling_on_429_7d_threshold_percent":"98"}`} {
		cfg := DefaultOAuthAutoConfig()
		require.Error(t, json.Unmarshal([]byte(raw), &cfg))
	}
}

func TestWeekly429ThresholdAppliesBeforeGatewayAndBPSPersistence(t *testing.T) {
	for _, bps := range []bool{false, true} {
		for _, high := range []bool{false, true} {
			account := scheduling429Account()
			weekly := "20"
			if high {
				weekly = "99"
				account.Extra["codex_7d_used_percent"] = 20.0
			}
			headers := weekly429Headers(weekly, "100", false)
			repo := &scheduling429Repo{}
			rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			gateway := &OpenAIGatewayService{rateLimitService: rates}
			rates.SetAccountRuntimeBlocker(gateway)
			if bps {
				gateway.coolDownExcelBPS(t.Context(), account, "5", headers)
			} else {
				gateway.handleOpenAIAccountUpstreamError(t.Context(), account, 429, headers, nil)
			}
			if high {
				require.Equal(t, []int64{account.ID}, repo.ids)
			} else {
				require.Empty(t, repo.ids)
			}
		}
	}
}

func TestWeekly429ThresholdDoesNotGateTOTPRotation(t *testing.T) {
	account := scheduling429Account()
	account.Extra["codex_7d_used_percent"] = 20.0
	account.Extra[OpenAITOTPRotateOn429ExtraKey] = true
	repo, rotation := &scheduling429Repo{}, &totp429Recorder{}
	rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rates.openAITOTP429Rotator = rotation
	rates.HandleUpstreamError(t.Context(), account, 429, nil, nil)
	require.Empty(t, repo.ids)
	require.Equal(t, []int64{account.ID}, rotation.accounts)
	require.Equal(t, 1, repo.rateLimitedCalls)
}
