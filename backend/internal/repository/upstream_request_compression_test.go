package repository

import (
	"bytes"
	"compress/gzip"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/requestcapture"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func TestHTTPUpstreamRequestCompressionOnWire(t *testing.T) {
	payload := []byte(`{"input":"` + strings.Repeat("repeated prompt text ", 8192) + `"}`)
	for _, coding := range []string{"gzip", "zstd"} {
		t.Run(coding, func(t *testing.T) {
			received := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				encoded, err := io.ReadAll(r.Body)
				if !assertWireRequest(t, r, encoded, err, coding) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				received <- encoded
				// An explicit rejection is returned to the caller, not replayed plain.
				w.WriteHeader(http.StatusUnsupportedMediaType)
			}))
			t.Cleanup(server.Close)
			upstream := NewHTTPUpstream(&config.Config{Gateway: config.GatewayConfig{
				UpstreamRequestCompression: []config.UpstreamRequestCompressionRule{{Origin: server.URL, Encoding: coding}},
			}})
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/v1/messages", bytes.NewReader(payload))
			require.NoError(t, err)
			req.Header["content-type"] = []string{"application/json; charset=utf-8"}
			req.Header["content-length"] = []string{strconv.Itoa(len(payload))}
			req.Header.Set("Accept-Encoding", "identity")
			resp, err := upstream.Do(req, "", 1, 1)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode)
			encoded := <-received
			require.Less(t, len(encoded), len(payload)/10)
			var decoded io.ReadCloser
			if coding == "gzip" {
				decoded, err = gzip.NewReader(bytes.NewReader(encoded))
			} else {
				var zr *zstd.Decoder
				zr, err = zstd.NewReader(bytes.NewReader(encoded))
				if err == nil {
					decoded = zr.IOReadCloser()
				}
			}
			require.NoError(t, err)
			got, err := io.ReadAll(decoded)
			require.NoError(t, err)
			require.NoError(t, decoded.Close())
			require.Equal(t, payload, got)
			require.Empty(t, req.Header.Get("Content-Encoding"))
			require.Equal(t, int64(len(payload)), req.ContentLength)
		})
	}
}

func assertWireRequest(t *testing.T, r *http.Request, body []byte, err error, coding string) bool {
	t.Helper()
	if err != nil || r.ContentLength != int64(len(body)) || r.Header.Get("Content-Encoding") != coding || r.Header.Get("Accept-Encoding") != "identity" {
		t.Errorf("wire request: %d bytes read, length %d, content encoding %q, accept encoding %q, error %v",
			len(body), r.ContentLength, r.Header.Get("Content-Encoding"), r.Header.Get("Accept-Encoding"), err)
		return false
	}
	return true
}

func TestHTTPUpstreamRequestCompressionReevaluatesRedirect(t *testing.T) {
	payload := strings.Repeat("request content", 1024)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Header.Get("Content-Encoding") != "" || string(body) != payload {
			t.Error("redirected request did not carry the original unencoded body")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") != "zstd" {
			t.Error("first hop did not receive a compressed request")
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	upstream := NewHTTPUpstream(&config.Config{Gateway: config.GatewayConfig{
		UpstreamRequestCompression: []config.UpstreamRequestCompressionRule{{Origin: redirector.URL, Encoding: "zstd"}},
	}})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, redirector.URL, strings.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := upstream.Do(req, "", 1, 1)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}

type compressionTestTransport func(*http.Request) (*http.Response, error)

func (f compressionTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenAICodexUpstreamRequestCompressionPolicy(t *testing.T) {
	const codexURL = "https://chatgpt.com/backend-api/codex/responses"
	payload := strings.Repeat("codex request ", 512)
	for _, tc := range []struct {
		name, target, mode, override, want string
		codex                              bool
	}{
		{name: "default zstd", target: codexURL, codex: true, want: "zstd"},
		{name: "explicit gzip", target: codexURL, codex: true, mode: "gzip", want: "gzip"},
		{name: "rollback to plain", target: codexURL, codex: true, mode: "off"},
		{name: "origin disables default", target: codexURL, codex: true, override: "off"},
		{name: "origin overrides mode", target: codexURL, codex: true, mode: "off", override: "gzip", want: "gzip"},
		{name: "unmarked first-party request", target: codexURL},
		{name: "public API", target: "https://api.openai.com/v1/responses", codex: true},
		{name: "compact", target: codexURL + "/compact", codex: true},
		{name: "lookalike", target: "https://chatgpt.com.example.net/backend-api/codex/responses", codex: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.codex {
				ctx = service.WithOpenAICodexHTTPRequest(ctx)
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, tc.target, strings.NewReader(payload))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			upstream := httpUpstreamService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAICodexRequestCompression: tc.mode}}}
			if tc.override != "" {
				upstream.cfg.Gateway.UpstreamRequestCompression = []config.UpstreamRequestCompressionRule{{Origin: "https://chatgpt.com", Encoding: tc.override}}
			}
			calls := 0
			transport := upstream.wrapUpstreamRequestCompression(compressionTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, tc.want, r.Header.Get("Content-Encoding"))
				defer r.Body.Close()
				if tc.want == "" {
					got, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.Equal(t, payload, string(got))
				} else {
					require.Less(t, r.ContentLength, int64(len(payload)))
				}
				return &http.Response{StatusCode: http.StatusUnsupportedMediaType, Body: http.NoBody}, nil
			}))
			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			require.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode)
			require.Equal(t, 1, calls, "a rejection must not replay the POST")
		})
	}
}

func TestUpstreamRequestCompressionSkipsIneligibleBodies(t *testing.T) {
	const origin = "https://example.com"
	payload := bytes.Repeat([]byte("repeated content"), 512)
	random := make([]byte, len(payload))
	_, _ = rand.NewChaCha8([32]byte{}).Read(random)
	for _, tc := range []struct {
		name   string
		body   []byte
		mutate func(*http.Request)
	}{
		{name: "small", body: []byte(`{"input":"hi"}`)},
		{name: "incompressible", body: random},
		{name: "multipart", mutate: func(r *http.Request) { r.Header.Set("Content-Type", "multipart/form-data; boundary=test") }},
		{name: "already encoded", mutate: func(r *http.Request) { r.Header["content-encoding"] = []string{"gzip"} }},
		{name: "integrity protected", mutate: func(r *http.Request) { r.Header.Set("Content-Digest", "test") }},
		{name: "not replayable", mutate: func(r *http.Request) { r.GetBody = nil }},
		{name: "local compression failure", mutate: func(r *http.Request) {
			r.GetBody = func() (io.ReadCloser, error) { return nil, io.ErrUnexpectedEOF }
		}},
		{name: "active capture", mutate: func(r *http.Request) {
			*r = *r.WithContext(requestcapture.WithSession(r.Context(), &requestcapture.Session{}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if body == nil {
				body = payload
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, origin, bytes.NewReader(body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			if tc.mutate != nil {
				tc.mutate(req)
			}
			transport := upstreamRequestCompressionTransport{
				rules: []config.UpstreamRequestCompressionRule{{Origin: origin, Encoding: "gzip"}},
				base: compressionTestTransport(func(got *http.Request) (*http.Response, error) {
					require.Same(t, req, got)
					defer got.Body.Close()
					b, err := io.ReadAll(got.Body)
					require.NoError(t, err)
					require.Equal(t, body, b)
					return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
				}),
			}
			_, err = transport.RoundTrip(req)
			require.NoError(t, err)
		})
	}
}
