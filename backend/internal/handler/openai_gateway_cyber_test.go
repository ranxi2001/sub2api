package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// newTestGinContext builds a bare gin.Context backed by an httptest recorder.
func newTestGinContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	return c
}

// TestRecordCyberPolicyIfMarked_NoMark verifies that when no cyber mark is set,
// the function returns immediately and does NOT set the recorded flag.
func TestRecordCyberPolicyIfMarked_NoMark(t *testing.T) {
	c := newTestGinContext()
	h := &OpenAIGatewayHandler{}

	h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", true, nil, service.ChannelUsageFields{}, "")

	// Flag must NOT be set when there was no mark.
	require.False(t, c.GetBool(cyberPolicyRecordedKey),
		"cyberPolicyRecordedKey must remain false when no cyber mark is present")
}

// TestRecordCyberPolicyIfMarked_WithMark verifies that:
//  1. When a cyber mark is present, the recorded flag is set (guard activated).
//  2. A second call is a no-op (idempotent guard).
//  3. Nil services do not panic.
func TestRecordCyberPolicyIfMarked_WithMark(t *testing.T) {
	c := newTestGinContext()
	service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{
		Message:        "flagged",
		Body:           `{"error":{"code":"cyber_policy"}}`,
		UpstreamStatus: 400,
	})

	h := &OpenAIGatewayHandler{} // nil services — must not panic

	// First call: should set the flag.
	require.NotPanics(t, func() {
		h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", true, nil, service.ChannelUsageFields{}, "")
	})
	require.True(t, c.GetBool(cyberPolicyRecordedKey),
		"cyberPolicyRecordedKey must be true after first call with a mark")

	// Second call: flag already set — must be a no-op (idempotent).
	require.NotPanics(t, func() {
		h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", false, nil, service.ChannelUsageFields{}, "")
	})
	// Flag should still be true (not toggled or cleared).
	require.True(t, c.GetBool(cyberPolicyRecordedKey),
		"cyberPolicyRecordedKey must remain true after second call (guard)")
}

// TestRecordCyberPolicyIfMarked_ForwardSuccessSkipsUsageLog verifies the semantic:
// when forwardErrored=false the function still sets the guard flag (mark present),
// but the cyber usage row is NOT requested (only RecordCyberPolicyEvent fires).
// Since services are nil here we only verify the guard flag and no panic.
func TestRecordCyberPolicyIfMarked_ForwardSuccessSkipsUsageLog(t *testing.T) {
	c := newTestGinContext()
	service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{
		Message:        "flagged",
		UpstreamStatus: 200,
	})

	h := &OpenAIGatewayHandler{}

	require.NotPanics(t, func() {
		h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", false /* forwardErrored=false */, nil, service.ChannelUsageFields{}, "")
	})
	require.True(t, c.GetBool(cyberPolicyRecordedKey))
}

// TestClearCyberPolicyTurnState verifies F1 at the handler level: after a turn
// is finalized, both the mark and the recorded guard are reset so the next WS
// turn detects/records independently.
func TestClearCyberPolicyTurnState(t *testing.T) {
	c := newTestGinContext()
	h := &OpenAIGatewayHandler{}

	service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{Message: "turn1", UpstreamStatus: 200})
	h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", false, nil, service.ChannelUsageFields{}, "")
	require.True(t, c.GetBool(cyberPolicyRecordedKey))

	clearCyberPolicyTurnState(c)
	require.Nil(t, service.GetOpsCyberPolicy(c))
	require.False(t, c.GetBool(cyberPolicyRecordedKey))

	// turn2: a fresh cyber hit must be recordable again.
	service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{Message: "turn2", UpstreamStatus: 200})
	h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", false, nil, service.ChannelUsageFields{}, "")
	require.True(t, c.GetBool(cyberPolicyRecordedKey))
	require.Equal(t, "turn2", service.GetOpsCyberPolicy(c).Message)
}

func TestAdvanceOpenAIWSCyberBlockStateDefersBlockAcrossFailover(t *testing.T) {
	failoverErr := &service.UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}

	blocked, pending := advanceOpenAIWSCyberBlockState(false, false, true, failoverErr)
	require.False(t, blocked, "the replacement account must receive the current turn")
	require.True(t, pending, "the cyber hit must still block later turns")

	blocked, pending = advanceOpenAIWSCyberBlockState(blocked, pending, false, failoverErr)
	require.False(t, blocked, "additional failover attempts must remain eligible")
	require.True(t, pending)

	blocked, pending = advanceOpenAIWSCyberBlockState(blocked, pending, false, nil)
	require.True(t, blocked, "the next client turn must be blocked after failover finishes")
	require.False(t, pending)
}

func TestClearCyberPolicyAttemptStatePreservesRecordedGuardDuringFailover(t *testing.T) {
	c := newTestGinContext()
	h := &OpenAIGatewayHandler{}

	service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{Message: "account-a", UpstreamStatus: http.StatusOK})
	h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", true, nil, service.ChannelUsageFields{}, "")
	require.True(t, c.GetBool(cyberPolicyRecordedKey))

	clearCyberPolicyAttemptState(c, false)
	require.Nil(t, service.GetOpsCyberPolicy(c))
	require.True(t, c.GetBool(cyberPolicyRecordedKey), "the same logical turn must not record again after failover")

	service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{Message: "account-b", UpstreamStatus: http.StatusOK})
	h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", true, nil, service.ChannelUsageFields{}, "")
	require.Equal(t, "account-b", service.GetOpsCyberPolicy(c).Message)
	require.True(t, c.GetBool(cyberPolicyRecordedKey))

	clearCyberPolicyAttemptState(c, true)
	require.Nil(t, service.GetOpsCyberPolicy(c))
	require.False(t, c.GetBool(cyberPolicyRecordedKey), "a completed logical turn must reset the guard")
}

// TestBuildCyberSessionBlockedOpsEntry verifies the locally-rejected request is
// auditable: 403 / phase=request / type=cyber_policy_session_blocked — distinct
// from upstream cyber_policy hits, and it must NOT touch moderation/violation.
func TestBuildCyberSessionBlockedOpsEntry(t *testing.T) {
	entry := buildCyberSessionBlockedOpsEntry(cyberPolicyOpsErrorMeta{
		RequestID: "req-9", Model: "gpt-5", RequestPath: "/openai/v1/responses",
	})
	require.Equal(t, 403, entry.StatusCode)
	require.Equal(t, "cyber_policy_session_blocked", entry.ErrorType)
	require.Equal(t, "request", entry.ErrorPhase)
	require.True(t, entry.IsBusinessLimited)
	require.Equal(t, "gateway_local", entry.ErrorSource)
	require.Equal(t, "platform", entry.ErrorOwner)
	require.Empty(t, entry.ErrorBody, "no session block key → ErrorBody must be empty")

	entryWithKey := buildCyberSessionBlockedOpsEntry(cyberPolicyOpsErrorMeta{
		RequestID: "req-9", Model: "gpt-5", RequestPath: "/openai/v1/responses",
		SessionBlockKey: "abc123",
	})
	require.Equal(t, "session_block_key=abc123", entryWithKey.ErrorBody)

	identityRejected := buildCyberSessionIdentityRejectedOpsEntry(cyberPolicyOpsErrorMeta{
		RequestID:      "req-10",
		Model:          "gpt-5",
		RequestPath:    "/openai/v1/responses",
		IdentityStatus: string(service.OpenAIClientSessionIdentityConflict),
		IdentityKind:   "thread",
		IdentitySource: service.OpenAIClientSessionIdentitySourceHeaderBody,
	})
	require.Equal(t, http.StatusBadRequest, identityRejected.StatusCode)
	require.Equal(t, "cyber_session_identity_rejected", identityRejected.ErrorType)
	require.Equal(t, "client", identityRejected.ErrorOwner)
	require.Contains(t, identityRejected.ErrorMessage, "session_identity_status=conflict")
	require.Contains(t, identityRejected.ErrorMessage, "session_identity_kind=thread")
	require.Contains(t, identityRejected.ErrorMessage, "session_identity_source=header_body")
}

// TestRejectIfCyberSessionBlocked_FailOpen verifies fail-open paths: nil handler
// services, no explicit session signal, and (implicitly) disabled switch all
// pass the request through.
func TestRejectIfCyberSessionBlocked_FailOpen(t *testing.T) {
	c := newTestGinContext()
	c.Request = httptest.NewRequest("POST", "/openai/v1/responses", strings.NewReader(`{}`))

	h := &OpenAIGatewayHandler{}
	require.False(t, h.rejectIfCyberSessionBlocked(c, nil, []byte(`{}`), "gpt-5", cyberBlockFormatResponses), "nil apiKey → pass")

	h2 := &OpenAIGatewayHandler{gatewayService: nil}
	key := &service.APIKey{ID: 1}
	require.False(t, h2.rejectIfCyberSessionBlocked(c, key, []byte(`{}`), "gpt-5", cyberBlockFormatResponses), "nil gateway service → pass")
}

func newCyberIdentityAdmissionTestHandler(settings map[string]string) *OpenAIGatewayHandler {
	return newCyberIdentityAdmissionTestHandlerWithCache(settings, nil)
}

func newCyberIdentityAdmissionTestHandlerWithCache(settings map[string]string, cache service.GatewayCache) *OpenAIGatewayHandler {
	settingSvc := service.NewSettingService(&contentModerationHandlerSettingRepo{values: settings}, nil)
	cfg := &config.Config{}
	gatewaySvc := service.NewOpenAIGatewayService(
		nil, nil, nil, nil, nil, nil, nil, cache, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, nil, nil, &service.DeferredService{},
		nil, nil, nil, nil, nil, settingSvc, nil,
	)
	return &OpenAIGatewayHandler{gatewayService: gatewaySvc}
}

func TestRejectIfCyberSessionBlocked_StrictIdentityGate(t *testing.T) {
	apiKey := &service.APIKey{ID: 77, Key: "sk-test", User: &service.User{ID: 9}}

	t.Run("strict off remains compatible", func(t *testing.T) {
		h := newCyberIdentityAdmissionTestHandler(map[string]string{
			service.SettingKeyCyberSessionBlockEnabled:          "true",
			service.SettingKeyCyberSessionIdentityStrictEnabled: "false",
		})
		c := newTestGinContext()
		c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{}`))
		require.False(t, h.rejectIfCyberSessionBlocked(c, apiKey, []byte(`{}`), "gpt-5", cyberBlockFormatResponses))
		require.Equal(t, http.StatusOK, c.Writer.Status())
	})

	t.Run("missing identity rejected before routing", func(t *testing.T) {
		h := newCyberIdentityAdmissionTestHandler(map[string]string{
			service.SettingKeyCyberSessionBlockEnabled:          "true",
			service.SettingKeyCyberSessionIdentityStrictEnabled: "true",
		})
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{}`))
		require.True(t, h.rejectIfCyberSessionBlocked(c, apiKey, []byte(`{}`), "gpt-5", cyberBlockFormatResponses))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), `"code":"cyber_session_identity_required"`)
	})

	t.Run("conflicting identity rejected", func(t *testing.T) {
		h := newCyberIdentityAdmissionTestHandler(map[string]string{
			service.SettingKeyCyberSessionBlockEnabled:          "true",
			service.SettingKeyCyberSessionIdentityStrictEnabled: "true",
		})
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"client_metadata":{"thread_id":"body"}}`))
		c.Request.Header.Set("thread_id", "header")
		require.True(t, h.rejectIfCyberSessionBlocked(c, apiKey, []byte(`{"client_metadata":{"thread_id":"body"}}`), "gpt-5", cyberBlockFormatResponses))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), `"code":"cyber_session_identity_conflict"`)
	})

	t.Run("resolved identity passes when not blocked", func(t *testing.T) {
		h := newCyberIdentityAdmissionTestHandler(map[string]string{
			service.SettingKeyCyberSessionBlockEnabled:          "true",
			service.SettingKeyCyberSessionIdentityStrictEnabled: "true",
		})
		c := newTestGinContext()
		c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"client_metadata":{"session_id":"session-a"}}`))
		require.False(t, h.rejectIfCyberSessionBlocked(c, apiKey, []byte(`{"client_metadata":{"session_id":"session-a"}}`), "gpt-5", cyberBlockFormatResponses))
	})
}

func TestWriteCyberSessionIdentityRejectedResponseFormats(t *testing.T) {
	apiKey := &service.APIKey{ID: 77, Key: "sk-test", User: &service.User{ID: 9}}
	metadata := service.OpenAIClientSessionIdentityMetadata{
		Status: service.OpenAIClientSessionIdentityConflict,
		Kind:   "thread",
		Source: service.OpenAIClientSessionIdentitySourceHeaderBody,
	}

	t.Run("responses and chat use OpenAI JSON envelope", func(t *testing.T) {
		for _, format := range []cyberSessionBlockFormat{cyberBlockFormatResponses, cyberBlockFormatChat} {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)

			(&OpenAIGatewayHandler{}).writeCyberSessionIdentityRejected(c, apiKey, "gpt-5", format, metadata)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, "cyber_session_identity_conflict", gjson.Get(rec.Body.String(), "error.code").String())
			require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
		}
	})

	t.Run("messages uses Anthropic JSON envelope", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/messages", nil)

		(&OpenAIGatewayHandler{}).writeCyberSessionIdentityRejected(c, apiKey, "gpt-5", cyberBlockFormatAnthropic, metadata)

		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "error", gjson.Get(rec.Body.String(), "type").String())
		require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
		require.False(t, gjson.Get(rec.Body.String(), "error.code").Exists())
	})

	t.Run("committed compact stream uses response failed event", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
		service.MarkOpenAICompactClientStream(c)

		stop := service.StartOpenAICompactSSEKeepalive(c, time.Millisecond)
		defer stop()
		require.Eventually(t, c.Writer.Written, time.Second, time.Millisecond)

		(&OpenAIGatewayHandler{}).writeCyberSessionIdentityRejected(c, apiKey, "gpt-5", cyberBlockFormatResponses, metadata)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), "event: response.failed\n")
		require.Contains(t, rec.Body.String(), `"code":"cyber_session_identity_conflict"`)
		require.NotContains(t, rec.Body.String(), `"error":{"type":"invalid_request_error","code":"cyber_session_identity_conflict"`)
	})
}

func TestOpenAIWSCyberIdentityBinding(t *testing.T) {
	c := newTestGinContext()
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	binding := &openAIWSCyberIdentityBinding{}

	first := binding.resolve(7, c, []byte(`{"client_metadata":{"thread_id":"thread-a"}}`), true, false)
	require.False(t, first.reject)
	require.True(t, first.effective.Resolved())
	require.False(t, first.effective.Inherited)

	missingFollowup := binding.resolve(7, c, []byte(`{"input":"follow-up"}`), true, true)
	require.False(t, missingFollowup.reject, "strict mode accepts a turn inherited from the verified connection identity")
	require.True(t, missingFollowup.effective.Resolved())
	require.True(t, missingFollowup.effective.Inherited)
	require.Equal(t, first.effective.BlockKey, missingFollowup.effective.BlockKey)

	swapped := binding.resolve(7, c, []byte(`{"client_metadata":{"thread_id":"thread-b"}}`), true, false)
	require.True(t, swapped.reject)
	require.True(t, swapped.identitySwap)

	c.Request.Header.Set("thread_id", "thread-c")
	conflicting := binding.resolve(7, c, []byte(`{"client_metadata":{"thread_id":"thread-b"}}`), true, false)
	require.True(t, conflicting.reject, "ambiguous follow-up must not inherit a trusted connection identity")

	c.Request.Header.Del("thread_id")
	strictBinding := &openAIWSCyberIdentityBinding{}
	strictMissing := strictBinding.resolve(7, c, []byte(`{}`), true, true)
	require.True(t, strictMissing.reject)
	require.Equal(t, service.OpenAIClientSessionIdentityMissing, strictMissing.observed.Metadata.Status)

	staticHeaderCtx := newTestGinContext()
	staticHeaderCtx.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	staticHeaderCtx.Request.Header.Set("session_id", "connection-session")
	staticHeaderBinding := &openAIWSCyberIdentityBinding{}
	bodyThread := staticHeaderBinding.resolve(7, staticHeaderCtx, []byte(`{"client_metadata":{"thread_id":"thread-from-first-frame"}}`), true, true)
	require.False(t, bodyThread.reject)
	require.Equal(t, "thread", bodyThread.effective.Metadata.Kind)
	staticHeaderFollowup := staticHeaderBinding.resolve(7, staticHeaderCtx, []byte(`{}`), true, true)
	require.False(t, staticHeaderFollowup.reject, "a lower-priority static upgrade header must not look like an explicit identity switch")
	require.True(t, staticHeaderFollowup.effective.Inherited)
	require.Equal(t, bodyThread.effective.BlockKey, staticHeaderFollowup.effective.BlockKey)

	disabledBinding := &openAIWSCyberIdentityBinding{}
	disabled := disabledBinding.resolve(7, c, []byte(`{}`), false, true)
	require.False(t, disabled.reject, "strict setting is inert while cyber session blocking is disabled")
}

// TestRecordCyberPolicyIfMarked_BlockKeyPlumbed verifies the 6th param is
// accepted and a non-empty key with nil gateway service does not panic
// (write-side guards live in the service layer).
func TestRecordCyberPolicyIfMarked_BlockKeyPlumbed(t *testing.T) {
	c := newTestGinContext()
	service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{Message: "x", UpstreamStatus: 400})
	h := &OpenAIGatewayHandler{}
	require.NotPanics(t, func() {
		h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", true, []byte(`{"input":"deadbeef"}`), service.ChannelUsageFields{}, "")
	})
}

// TestBuildCyberPolicyOpsErrorEntry_StatusCode verifies F6: the ops error log
// records the status the codex client actually received (400 non-stream / 200 stream),
// not a hardcoded 403.
func TestBuildCyberPolicyOpsErrorEntry_StatusCode(t *testing.T) {
	for _, tc := range []struct {
		name           string
		upstreamStatus int
	}{
		{"non_stream_400", 400},
		{"stream_200", 200},
		{"zero_value", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mark := &service.CyberPolicyMark{
				Code:           "cyber_policy",
				Message:        "blocked",
				UpstreamStatus: tc.upstreamStatus,
			}
			entry := buildCyberPolicyOpsErrorEntry(cyberPolicyOpsErrorMeta{
				RequestID: "req-1", Model: "gpt-5", RequestPath: "/openai/v1/responses",
			}, mark)
			require.Equal(t, tc.upstreamStatus, entry.StatusCode)
			require.Equal(t, "cyber_policy", entry.ErrorType)
			require.Equal(t, "request", entry.ErrorPhase)
		})
	}
}

func TestBuildCyberPolicyOpsErrorEntryIncludesIdentityMetadataWithoutRawID(t *testing.T) {
	entry := buildCyberPolicyOpsErrorEntry(cyberPolicyOpsErrorMeta{
		IdentityStatus:    string(service.OpenAIClientSessionIdentityResolved),
		IdentityKind:      "thread",
		IdentitySource:    service.OpenAIClientSessionIdentitySourceConnection,
		IdentityInherited: true,
	}, &service.CyberPolicyMark{
		Message:        "blocked",
		UpstreamStatus: http.StatusBadRequest,
	})

	require.Contains(t, entry.ErrorMessage, "session_identity_status=resolved")
	require.Contains(t, entry.ErrorMessage, "session_identity_kind=thread")
	require.Contains(t, entry.ErrorMessage, "session_identity_source=connection")
	require.Contains(t, entry.ErrorMessage, "session_identity_inherited=true")
	require.NotContains(t, entry.ErrorMessage, "private-thread-value")
}
