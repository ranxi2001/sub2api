package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/util/transportdiag"
)

// Status errors contain fixed classifications, never response bodies or URLs.
type bpsProbeError struct{ reason string }

func (e *bpsProbeError) Error() string { return "BPS qualification failed: " + e.reason }
func bpsProbeFailureReason(err error) string {
	var rejection *bpsProbeError
	if errors.As(err, &rejection) {
		return rejection.reason
	}
	return transportdiag.Classify(err)
}

func probeBPSHTTPSClient(ctx context.Context, client *http.Client) error {
	// A root-page 403/404 proves TLS only. This fixed unauthenticated request
	// checks the actual route without invoking a model or sending account data.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://bps.openai.com/basispoints/api/responses", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	reason := "bps_unexpected_status"
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		contentType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			return &bpsProbeError{"bps_invalid_auth_challenge"}
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024+1))
		if err != nil {
			return err
		}
		if len(raw) > 16*1024 {
			return &bpsProbeError{"bps_probe_body_too_large"}
		}
		var body map[string]json.RawMessage
		if json.Unmarshal(raw, &body) != nil {
			return &bpsProbeError{"bps_invalid_auth_challenge"}
		}
		value, ok := body["error"]
		if !ok || string(value) == "null" || len(value) == 0 {
			return &bpsProbeError{"bps_invalid_auth_challenge"}
		}
		return nil
	case http.StatusForbidden:
		reason = "bps_access_denied"
	case http.StatusProxyAuthRequired:
		reason = "proxy_auth_rejected"
	case http.StatusTooManyRequests:
		reason = "bps_probe_rate_limited"
	default:
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			reason = "bps_probe_redirect"
		}
		if resp.StatusCode >= 500 {
			reason = "bps_probe_upstream_unavailable"
		}
	}
	return &bpsProbeError{reason}
}
