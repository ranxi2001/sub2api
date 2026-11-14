//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type recordingTempUnschedRepo struct {
	AccountRepository
	reasons      []string
	modelReasons []string
}

func (r *recordingTempUnschedRepo) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, reason string) error {
	r.reasons = append(r.reasons, reason)
	return nil
}

func (r *recordingTempUnschedRepo) SetModelRateLimit(_ context.Context, _ int64, _ string, _ time.Time, reason ...string) error {
	if len(reason) > 0 {
		r.modelReasons = append(r.modelReasons, reason[0])
	} else {
		r.modelReasons = append(r.modelReasons, "")
	}
	return nil
}

func (r *recordingTempUnschedRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	return &Account{ID: id}, nil
}

func (r *recordingTempUnschedRepo) SetOverloaded(context.Context, int64, time.Time) error { return nil }
func (r *recordingTempUnschedRepo) SetRateLimited(context.Context, int64, time.Time) error {
	return nil
}

func tempUnschedTimingAccount(rules ...map[string]any) *Account {
	raw := make([]any, 0, len(rules))
	for _, rule := range rules {
		raw = append(raw, rule)
	}
	return &Account{
		ID:       77,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules":   raw,
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func TestGetTempUnschedulableRules_MissingFieldsKeepLegacyDefaults(t *testing.T) {
	account := tempUnschedTimingAccount(map[string]any{
		"error_code":       float64(502),
		"keywords":         []any{"bad gateway"},
		"duration_minutes": float64(5),
		"description":      "legacy",
	})

	rules := account.GetTempUnschedulableRules()
	require.Len(t, rules, 1)
	require.Empty(t, rules[0].StreamTiming)
	require.Equal(t, 1, rules[0].NormalizedTriggerCount())
	require.Equal(t, 1, rules[0].TriggerWindowMinutes(), "omitted trigger_window_minutes does not borrow duration_minutes")
	require.Equal(t, 0, rules[0].TriggerCount, "omitted trigger_count stays unset so callers can tell it was missing")
}

func TestGetTempUnschedulableRules_ParsesExplicitTimingAndCount(t *testing.T) {
	account := tempUnschedTimingAccount(map[string]any{
		"error_code":             "502",
		"keywords":               []any{"upstream_error"},
		"duration_minutes":       float64(8),
		"stream_timing":          " AFTER_STREAM ",
		"trigger_count":          "3",
		"trigger_window_minutes": "15",
		"scope":                  "account",
	})

	rules := account.GetTempUnschedulableRules()
	require.Len(t, rules, 1)
	require.Equal(t, TempUnschedStreamAfter, rules[0].StreamTiming)
	require.Equal(t, 3, rules[0].NormalizedTriggerCount())
	require.Equal(t, 15, rules[0].TriggerWindowMinutes())
	require.Equal(t, TempUnschedScopeAccount, rules[0].Scope)

	unknown := tempUnschedTimingAccount(map[string]any{
		"error_code":       float64(429),
		"keywords":         []any{"rate limit"},
		"duration_minutes": float64(2),
		"stream_timing":    "whenever",
		"trigger_count":    float64(0),
	})
	parsed := unknown.GetTempUnschedulableRules()
	require.Empty(t, parsed[0].StreamTiming, "unrecognized timing stays legacy")
	require.Equal(t, 1, parsed[0].NormalizedTriggerCount())
}

func TestAttemptTempUnschedulable_StreamPhaseAndTriggerCount(t *testing.T) {
	body := []byte(`{"error":{"type":"upstream_error","message":"bad gateway"}}`)
	rateBody := []byte(`{"error":{"message":"rate limit exceeded"}}`)
	repo := &recordingTempUnschedRepo{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	clock := now
	svc.SetTempUnschedTriggerCounter(newMemoryTempUnschedTriggerCounter(func() time.Time { return clock }))

	legacy502 := tempUnschedTimingAccount(map[string]any{
		"error_code":       float64(502),
		"keywords":         []any{"bad gateway"},
		"duration_minutes": float64(5),
		"scope":            "account",
	})
	require.Equal(t, tempUnschedTriggered, svc.attemptTempUnschedulable(context.Background(), legacy502, http.StatusBadGateway, body))
	require.Len(t, repo.reasons, 1)
	repo.reasons = nil

	require.Equal(t, tempUnschedNoMatch, svc.attemptTempUnschedulable(
		withTempUnschedStreamPhase(context.Background(), tempUnschedPhaseMidStreamOptIn),
		legacy502, http.StatusBadGateway, body,
	), "omitted stream_timing must not match generic mid-stream 502")
	repo.reasons = nil
	require.Equal(t, tempUnschedTriggered, svc.attemptTempUnschedulable(
		withTempUnschedStreamPhase(context.Background(), tempUnschedPhaseMidStreamLegacy),
		legacy502, http.StatusBadGateway, body,
	), "legacy phase is the existing 401/429/503/529 side-effect path and still matches omitted timing")

	before502 := tempUnschedTimingAccount(map[string]any{
		"error_code":       float64(502),
		"keywords":         []any{"bad gateway"},
		"duration_minutes": float64(5),
		"scope":            "account",
		"stream_timing":    TempUnschedStreamBefore,
	})
	require.Equal(t, tempUnschedTriggered, svc.attemptTempUnschedulable(context.Background(), before502, http.StatusBadGateway, body))
	require.Equal(t, tempUnschedNoMatch, svc.attemptTempUnschedulable(
		withTempUnschedStreamPhase(context.Background(), tempUnschedPhaseMidStreamOptIn),
		before502, http.StatusBadGateway, body,
	))
	repo.reasons = nil

	legacy429 := tempUnschedTimingAccount(map[string]any{
		"error_code":       float64(429),
		"keywords":         []any{"rate limit"},
		"duration_minutes": float64(10),
		"scope":            "account",
	})
	midLegacy := withTempUnschedStreamPhase(context.Background(), tempUnschedPhaseMidStreamLegacy)
	require.Equal(t, tempUnschedTriggered, svc.attemptTempUnschedulable(midLegacy, legacy429, http.StatusTooManyRequests, rateBody))

	before429 := tempUnschedTimingAccount(map[string]any{
		"error_code":       float64(429),
		"keywords":         []any{"rate limit"},
		"duration_minutes": float64(10),
		"scope":            "account",
		"stream_timing":    TempUnschedStreamBefore,
	})
	require.Equal(t, tempUnschedNoMatch, svc.attemptTempUnschedulable(midLegacy, before429, http.StatusTooManyRequests, rateBody))
	require.Equal(t, tempUnschedTriggered, svc.attemptTempUnschedulable(context.Background(), before429, http.StatusTooManyRequests, rateBody))
	repo.reasons = nil

	counted := tempUnschedTimingAccount(map[string]any{
		"error_code":             float64(502),
		"keywords":               []any{"bad gateway"},
		"duration_minutes":       float64(10),
		"scope":                  "account",
		"stream_timing":          TempUnschedStreamAfter,
		"trigger_count":          float64(3),
		"trigger_window_minutes": float64(10),
	})
	optIn := withTempUnschedStreamPhase(context.Background(), tempUnschedPhaseMidStreamOptIn)
	require.Equal(t, tempUnschedPending, svc.attemptTempUnschedulable(optIn, counted, http.StatusBadGateway, body))
	require.Equal(t, tempUnschedPending, svc.attemptTempUnschedulable(optIn, counted, http.StatusBadGateway, body))
	require.Empty(t, repo.reasons)
	require.Equal(t, tempUnschedTriggered, svc.attemptTempUnschedulable(optIn, counted, http.StatusBadGateway, body))
	require.Len(t, repo.reasons, 1)
	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(repo.reasons[0]), &state))
	require.Equal(t, int64(3), state.TriggerCount)
	require.Equal(t, 3, state.TriggerThreshold)
	require.Equal(t, 10, state.TriggerWindowMinutes)

	// Cooldown resets the window, so the next matches start again at 1.
	repo.reasons = nil
	require.Equal(t, tempUnschedPending, svc.attemptTempUnschedulable(optIn, counted, http.StatusBadGateway, body))
	require.Empty(t, repo.reasons)

	// The fixed window expires with trigger_window_minutes, even if the threshold was not reached.
	clock = now.Add(11 * time.Minute)
	require.Equal(t, tempUnschedPending, svc.attemptTempUnschedulable(optIn, counted, http.StatusBadGateway, body))
	require.Equal(t, tempUnschedPending, svc.attemptTempUnschedulable(optIn, counted, http.StatusBadGateway, body))
	require.Empty(t, repo.reasons)
	require.Equal(t, tempUnschedTriggered, svc.attemptTempUnschedulable(optIn, counted, http.StatusBadGateway, body))
}

func TestOpenAIStreamTerminal_MidStream502OnlyWhenOptedIn(t *testing.T) {
	repo := &recordingTempUnschedRepo{}
	svc := &OpenAIGatewayService{rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil)}
	payload := []byte(`{"type":"error","error":{"type":"upstream_error","code":"upstream_error","message":"bad gateway"}}`)
	account := func(timing any, count any) *Account {
		rule := map[string]any{
			"error_code":       float64(http.StatusBadGateway),
			"keywords":         []any{"upstream_error", "bad gateway"},
			"duration_minutes": float64(5),
			"scope":            "account",
		}
		if timing != nil {
			rule["stream_timing"] = timing
		}
		if count != nil {
			rule["trigger_count"] = count
		}
		return tempUnschedTimingAccount(rule)
	}

	status, disabled := svc.handleOpenAIStreamTerminalAccountSideEffects(nil, account(nil, nil), payload, "bad gateway", nil, "gpt-test")
	require.Equal(t, http.StatusBadGateway, status)
	require.False(t, disabled)
	require.Empty(t, repo.reasons)

	status, disabled = svc.handleOpenAIStreamTerminalAccountSideEffects(nil, account(TempUnschedStreamBefore, nil), payload, "bad gateway", nil, "gpt-test")
	require.Equal(t, http.StatusBadGateway, status)
	require.False(t, disabled)
	require.Empty(t, repo.reasons)

	status, disabled = svc.handleOpenAIStreamTerminalAccountSideEffects(nil, account(TempUnschedStreamAfter, float64(2)), payload, "bad gateway", nil, "gpt-test")
	require.Equal(t, http.StatusBadGateway, status)
	require.False(t, disabled, "the first of two required matches must not cool the account")
	require.Empty(t, repo.reasons)
}

func TestOpenAIStreamTerminal_MidStream502OptInFiresOnSecondHit(t *testing.T) {
	repo := &recordingTempUnschedRepo{}
	limits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: limits}
	payload := []byte(`{"type":"error","error":{"type":"upstream_error","message":"bad gateway"}}`)
	account := tempUnschedTimingAccount(map[string]any{
		"error_code":             float64(http.StatusBadGateway),
		"keywords":               []any{"bad gateway"},
		"duration_minutes":       float64(5),
		"scope":                  "account",
		"stream_timing":          TempUnschedStreamAfter,
		"trigger_count":          float64(2),
		"trigger_window_minutes": float64(5),
	})

	_, disabled := svc.handleOpenAIStreamTerminalAccountSideEffects(nil, account, payload, "bad gateway", nil, "gpt-test")
	require.False(t, disabled)
	require.Empty(t, repo.reasons)

	status, disabled := svc.handleOpenAIStreamTerminalAccountSideEffects(nil, account, payload, "bad gateway", nil, "gpt-test")
	require.Equal(t, http.StatusBadGateway, status)
	require.True(t, disabled)
	require.Len(t, repo.reasons, 1)
	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(repo.reasons[0]), &state))
	require.Equal(t, int64(2), state.TriggerCount)
	require.Equal(t, 2, state.TriggerThreshold)
	require.Equal(t, 5, state.TriggerWindowMinutes)
}

func TestSSEStreamError_MidStreamOverloadRequiresOptIn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	repo := &recordingTempUnschedRepo{}
	svc := &GatewayService{rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil)}
	resp := &http.Response{Header: http.Header{}, StatusCode: http.StatusOK}
	body := `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	account := func(timing any) *Account {
		rule := map[string]any{
			"error_code":       float64(529),
			"keywords":         []any{"Overloaded"},
			"duration_minutes": float64(15),
			"scope":            "model",
		}
		if timing != nil {
			rule["stream_timing"] = timing
		}
		got := tempUnschedTimingAccount(rule)
		got.Platform = PlatformAnthropic
		return got
	}

	err := svc.sseStreamErrorEventFailover(context.Background(), c, account(nil), resp, body, "claude-sonnet", true)
	require.NotNil(t, err)
	require.Equal(t, http.StatusForbidden, err.StatusCode)
	require.Empty(t, repo.modelReasons)
	require.Empty(t, repo.reasons)

	_ = svc.sseStreamErrorEventFailover(context.Background(), c, account(TempUnschedStreamBefore), resp, body, "claude-sonnet", true)
	require.Empty(t, repo.modelReasons)

	_ = svc.sseStreamErrorEventFailover(context.Background(), c, account(TempUnschedStreamAfter), resp, body, "claude-sonnet", true)
	require.Len(t, repo.modelReasons, 1)
	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(repo.modelReasons[0]), &state))
	require.Equal(t, TempUnschedScopeModel, state.Scope)
	require.Equal(t, "claude-sonnet", state.Model)
	require.Equal(t, 529, state.StatusCode)
}
