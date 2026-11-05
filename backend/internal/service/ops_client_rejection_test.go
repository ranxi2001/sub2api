package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpsModelCapabilityRejection(t *testing.T) {
	for _, tc := range []struct {
		name             string
		status, upstream int
		message          string
		want             bool
	}{
		{"explicit missing code", 502, 404, "model_not_found", true},
		{"explicit unsupported code", 400, 400, "model_not_supported", true},
		{"missing model", 404, 404, "Model not found", true},
		{"capacity with misleading code", 503, 503, "model_not_found", false},
		{"codex model", 400, 400, "The 'example-model' model is not supported when using Codex with a ChatGPT account.", true},
		{"wrapped model", 502, 404, `Model "example-model" is not available for this group`, true},
		{"admission wrapper", 503, 404, `Model "example-model" is not supported by any configured account in this group`, true},
		{"supplier balance", 403, 403, "Insufficient account balance", false},
		{"provider credentials", 503, 401, "Your authentication token has expired", false},
		{"provider validation", 400, 400, "Invalid request: encrypted content could not be verified", false},
		{"missing upstream route", 404, 404, "Not Found", false},
		{"capacity", 503, 503, "No available channel for model example-model", false},
		{"admission", 503, 0, "Account eligibility changed; please retry with complete context", false},
		{"pod", 503, 0, "Bound Pod unavailable; the existing conversation was not moved", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, IsOpsModelCapabilityRejection(tc.status, tc.upstream, tc.message))
		})
	}
}

func TestNormalizeOpsClientRejectionPreservesProviderEvidence(t *testing.T) {
	status := 404
	message := `Model "example-model" is not available for this group`
	entry := &OpsInsertErrorLogInput{StatusCode: 502, ErrorOwner: "provider", ErrorPhase: "upstream",
		UpstreamStatusCode: &status, UpstreamErrorMessage: &message}
	NormalizeOpsClientRejection(entry)
	require.True(t, entry.IsBusinessLimited)
	require.Equal(t, "client", entry.ErrorOwner)
	require.Equal(t, "request", entry.ErrorPhase)
	require.Equal(t, &status, entry.UpstreamStatusCode)
	require.Equal(t, &message, entry.UpstreamErrorMessage)
	entry = &OpsInsertErrorLogInput{StatusCode: 400, ErrorOwner: "client", ErrorPhase: "request", ErrorType: "invalid_request_error"}
	NormalizeOpsClientRejection(entry)
	require.True(t, entry.IsBusinessLimited)
	entry.IsBusinessLimited = false
	entry.UpstreamStatusCode = &status
	NormalizeOpsClientRejection(entry)
	require.False(t, entry.IsBusinessLimited, "upstream validation is not automatically the user's fault")
}

func TestNormalizeOpsLocalRejectionTypes(t *testing.T) {
	for _, errorType := range []string{"invalid_request_error", "model_not_found", "billing_error"} {
		entry := &OpsInsertErrorLogInput{StatusCode: 403, ErrorOwner: "client", ErrorPhase: "request", ErrorType: errorType}
		NormalizeOpsClientRejection(entry)
		require.True(t, entry.IsBusinessLimited, errorType)
		entry.IsBusinessLimited, entry.StatusCode = false, 503
		NormalizeOpsClientRejection(entry)
		require.False(t, entry.IsBusinessLimited, "a failed local service is not a user rejection")
	}
}

func TestOpsContextLimitRejection(t *testing.T) {
	for _, tc := range []struct {
		status, upstream int
		message          string
		want             bool
	}{
		{400, 400, "prompt is too long: compact the conversation and retry.", true},
		{400, 0, "prompt is too long: compact the conversation and retry.", true},
		{502, 413, "maximum prompt length exceeded", true},
		{400, 400, "context_length_exceeded", true},
		{400, 400, "Request payload is too large", true},
		{413, 0, "Request payload is too large", true},
		{502, 413, "REQUEST PAYLOAD IS TOO LARGE", true},
		{422, 422, "Request payload is too large", true},
		{503, 503, "Request payload is too large", false},
		{502, 502, `response.failed: {"error":{"code":"context_length_exceeded"}}`, true},
		{502, 0, "CONTEXT_LENGTH_EXCEEDED", true},
		{503, 502, "context_length_exceeded", true},
		{502, 503, "context_length_exceeded", false},
		{503, 0, "context_length_exceeded", false},
		{502, 502, "contextXlengthYexceeded", false},
		{502, 502, "prompt is too long", false},
		{502, 502, "Bad Gateway", false},
		{502, 0, "Request payload is too large", false},
		{400, 400, "Upstream request failed", false},
		{503, 503, "prompt is too long; upstream service unavailable", false},
		{400, 400, "Encrypted content could not be verified", false},
	} {
		require.Equal(t, tc.want, IsOpsContextLimitRejection(tc.status, tc.upstream, tc.message))
	}
}

func TestNormalizeOpsResponsibilityKeepsOccurrenceLayer(t *testing.T) {
	upstreamStatus := 400
	message := "prompt is too long: compact the conversation and retry."
	entry := &OpsInsertErrorLogInput{StatusCode: 400, UpstreamStatusCode: &upstreamStatus,
		ErrorOwner: "provider", ErrorPhase: "upstream", ErrorSource: "upstream_http",
		ErrorType: "api_error", UpstreamErrorMessage: &message}
	NormalizeOpsClientRejection(entry)
	require.True(t, entry.IsBusinessLimited)
	require.Equal(t, "client", entry.ErrorOwner)
	require.Equal(t, "request", entry.ErrorPhase)
	require.Equal(t, "upstream_http", entry.ErrorSource)
	require.Equal(t, &upstreamStatus, entry.UpstreamStatusCode)
}

func TestNormalizeOpsPayloadLimitPreservesProviderEvidence(t *testing.T) {
	upstreamStatus := 400
	message := "Request payload is too large"
	entry := &OpsInsertErrorLogInput{StatusCode: 400, UpstreamStatusCode: &upstreamStatus,
		ErrorOwner: "provider", ErrorPhase: "upstream", ErrorSource: "upstream_http",
		ErrorType: "invalid_request_error", ErrorMessage: message, UpstreamErrorMessage: &message}
	NormalizeOpsClientRejection(entry)

	require.True(t, entry.IsBusinessLimited)
	require.Equal(t, "client", entry.ErrorOwner)
	require.Equal(t, "request", entry.ErrorPhase)
	require.Equal(t, "context_limit", entry.ErrorType)
	require.Equal(t, "upstream_http", entry.ErrorSource)
	require.Equal(t, &upstreamStatus, entry.UpstreamStatusCode)
	require.Equal(t, message, entry.ErrorMessage)
	require.Equal(t, &message, entry.UpstreamErrorMessage)
}

func TestOpsServicePersistsRequestResponsibility(t *testing.T) {
	var captured *OpsInsertErrorLogInput
	repo := &opsRepoMock{InsertErrorLogFn: func(_ context.Context, entry *OpsInsertErrorLogInput) (int64, error) {
		captured = entry
		return 1, nil
	}}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	status := 400
	message := "prompt is too long: compact the conversation and retry."
	err := svc.RecordError(context.Background(), &OpsInsertErrorLogInput{StatusCode: 400, UpstreamStatusCode: &status,
		ErrorOwner: "provider", ErrorPhase: "upstream", ErrorSource: "upstream_http", ErrorType: "api_error", UpstreamErrorMessage: &message})
	require.NoError(t, err)
	require.True(t, captured.IsBusinessLimited)
	require.Equal(t, "client", captured.ErrorOwner)
	require.Equal(t, "context_limit", captured.ErrorType)
	require.Equal(t, "upstream_http", captured.ErrorSource)
	require.Equal(t, status, *captured.UpstreamStatusCode)
}

func TestNormalizeOpsWrappedContextLimit(t *testing.T) {
	for _, tc := range []struct {
		name, phase      string
		status, upstream int
		message, detail  string
		want             bool
	}{
		{"message code", "upstream", 502, 502, "response.failed: context_length_exceeded", "", true},
		{"detail code", "upstream", 502, 502, "Upstream request failed", `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded"}}}`, true},
		{"wrapped final status", "upstream", 503, 502, "", "CONTEXT_LENGTH_EXCEEDED", true},
		{"generic gateway", "upstream", 502, 502, "Bad Gateway", "", false},
		{"vague payload", "upstream", 502, 502, "Request payload is too large", "", false},
		{"capacity status", "upstream", 502, 503, "", "context_length_exceeded", false},
		{"auth attribution", "account_auth", 502, 502, "context_length_exceeded", "", false},
		{"recovered attempt", "upstream", 200, 502, "context_length_exceeded", "", false},
		{"other detail only", "upstream", 400, 400, "Upstream request failed", "context_length_exceeded", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := &OpsInsertErrorLogInput{StatusCode: tc.status, UpstreamStatusCode: &tc.upstream,
				ErrorOwner: "provider", ErrorPhase: tc.phase, ErrorSource: "upstream_http", ErrorType: "upstream_error",
				UpstreamErrorMessage: &tc.message, UpstreamErrorDetail: &tc.detail}
			NormalizeOpsClientRejection(entry)
			require.Equal(t, tc.want, entry.IsBusinessLimited)
			if tc.want {
				require.Equal(t, "client", entry.ErrorOwner)
				require.Equal(t, "request", entry.ErrorPhase)
				require.Equal(t, "context_limit", entry.ErrorType)
			} else {
				require.Equal(t, "provider", entry.ErrorOwner)
				require.Equal(t, tc.phase, entry.ErrorPhase)
				require.Equal(t, "upstream_error", entry.ErrorType)
			}
			require.Equal(t, tc.status, entry.StatusCode)
			require.Equal(t, tc.upstream, *entry.UpstreamStatusCode)
			require.Equal(t, tc.message, *entry.UpstreamErrorMessage)
			require.Equal(t, tc.detail, *entry.UpstreamErrorDetail)
			require.Equal(t, "upstream_http", entry.ErrorSource)
		})
	}
}
