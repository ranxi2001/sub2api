package service

import (
	"bytes"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// Used identically against the full baseline package and candidate package in
// remote CI. The request builder, admission and bridge run without substitutes;
// this final port sends to a real loopback HTTP/SSE server.
type issue605LegacyNetworkProbe struct {
	client   *http.Client
	t        *testing.T
	start    time.Time
	lastReq  *http.Request
	lastBody []byte
	calls    atomic.Int32
}

func (p *issue605LegacyNetworkProbe) mark(stage string) {
	p.t.Logf("ISSUE605_LEGACY_PROBE stage=%s elapsed_ms=%d calls=%d", stage, time.Since(p.start).Milliseconds(), p.calls.Load())
}
func (p *issue605LegacyNetworkProbe) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	p.calls.Add(1)
	p.lastReq = r
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	p.lastBody = raw
	r.Body = io.NopCloser(bytes.NewReader(raw))
	p.mark("http_send")
	return p.client.Do(r)
}
func (p *issue605LegacyNetworkProbe) DoWithTLS(r *http.Request, s string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return p.Do(r, s, id, n)
}
