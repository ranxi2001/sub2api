package mihomo

import (
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"testing"
)

type bpsQualificationTransport func(*http.Request) (*http.Response, error)

func (f bpsQualificationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBPSQualificationRejectsBlockedAndCaptiveResponses(t *testing.T) {
	for _, tc := range []struct {
		name              string
		status            int
		contentType, body string
		ok                bool
	}{
		{"real_auth_challenge", 401, "application/json", `{"error":{"code":"unauthorized"}}`, true},
		{"root_not_found", 404, "text/plain", "not found", false},
		{"blocked", 403, "application/json", `{"error":"denied"}`, false},
		{"challenge_html", 401, "text/html", "<html>verify</html>", false},
		{"proxy_auth", 407, "application/json", `{"error":"proxy auth"}`, false},
		{"redirect", 302, "text/html", "redirect", false},
		{"rate_limited", 429, "application/json", `{"error":"rate"}`, false},
		{"empty_error", 401, "application/json", `{"error":null}`, false},
		{"fake_success", 200, "application/json", "{}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: bpsQualificationTransport(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "https://bps.openai.com/basispoints/api/responses", r.URL.String())
				require.Empty(t, r.Header.Get("Authorization"))
				require.Empty(t, r.Header.Get("Chatgpt-Account-Id"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.Equal(t, "{}", string(body))
				require.NoError(t, r.Body.Close())
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			err := probeBPSHTTPSClient(t.Context(), client)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
