package repository

import (
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
	"github.com/Wei-Shaw/sub2api/internal/requestcapture"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Record native HTTP inference status before handing response headers to the caller.
// Opted-in 429 samples are queued for asynchronous persistence.
type accountResponseTransport struct{ base http.RoundTripper }

func (t *accountResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	started := time.Now().UTC()
	resp, err := t.base.RoundTrip(req)
	if err == nil && resp != nil && req.Context().Err() == nil && isAccountInferenceRequest(req) {
		upstreamstats.Default.Record(requestcapture.AccountFromContext(req.Context()), resp.StatusCode, time.Now())
		service.ObserveOAuth429HTTP(req, requestcapture.AccountFromContext(req.Context()), resp.StatusCode, started)
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
