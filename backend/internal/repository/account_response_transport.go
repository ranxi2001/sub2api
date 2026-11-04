package repository

import (
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
	"github.com/Wei-Shaw/sub2api/internal/requestcapture"
)

// Passive header-only observation shared by native HTTP transports (including
// TLS and regional egress). No body reads, retries, logging or storage IO.
type accountResponseTransport struct{ base http.RoundTripper }

func (t *accountResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil && resp != nil && req.Context().Err() == nil && isAccountInferenceRequest(req) {
		upstreamstats.Default.Record(requestcapture.AccountFromContext(req.Context()), resp.StatusCode, time.Now())
	}
	return resp, err
}

func isAccountInferenceRequest(req *http.Request) bool {
	if req.Method != http.MethodPost || req.URL == nil {
		return false
	}
	path := strings.TrimRight(req.URL.Path, "/")
	for _, suffix := range []string{"/messages", "/responses", "/responses/compact", "/chat/completions", "/completions", ":generateContent", ":streamGenerateContent"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}
