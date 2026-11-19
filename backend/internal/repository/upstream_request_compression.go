package repository

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/requestcapture"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/net/http/httpguts"
)

const (
	minUpstreamCompressionSize = 2 << 10
	maxUpstreamCompressionSize = 32 << 20
)

type upstreamRequestCompressionTransport struct {
	base      http.RoundTripper
	rules     []config.UpstreamRequestCompressionRule
	codexMode string
	cooldowns *sync.Map
}

type compressionRejection struct {
	path  string
	until time.Time
}

func (s *httpUpstreamService) wrapUpstreamRequestCompression(base http.RoundTripper) http.RoundTripper {
	if s.cfg == nil {
		return &upstreamRequestCompressionTransport{base: base, cooldowns: &s.compressionCooldowns}
	}
	gateway := &s.cfg.Gateway
	if gateway.OpenAICodexRequestCompression == "off" && len(gateway.UpstreamRequestCompression) == 0 {
		return base
	}
	return &upstreamRequestCompressionTransport{base: base, rules: gateway.UpstreamRequestCompression, codexMode: gateway.OpenAICodexRequestCompression, cooldowns: &s.compressionCooldowns}
}

func (t *upstreamRequestCompressionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.Opaque != "" {
		return t.base.RoundTrip(req)
	}
	origin := req.URL.Scheme + "://" + req.URL.Host
	coding := t.coding(req, origin)
	if (coding != "zstd" && coding != "gzip") || !canCompressUpstreamRequest(req) {
		return t.base.RoundTrip(req)
	}
	key := strings.ToLower(origin) + " " + coding
	if t.cooldowns != nil {
		if entry, ok := t.cooldowns.Load(key); ok {
			rejection, ok := entry.(compressionRejection)
			if ok && rejection.path == req.URL.EscapedPath() && time.Now().Before(rejection.until) {
				return t.base.RoundTrip(req)
			}
		}
	}

	var compressed bytes.Buffer
	if !compressUpstreamRequestBody(req, coding, &compressed) || int64(compressed.Len()) >= req.ContentLength {
		return t.base.RoundTrip(req)
	}

	// Apply the encoding per physical send. Redirects and provider fallbacks
	// continue to see the original body and re-evaluate the destination rule.
	defer func() { _ = req.Body.Close() }()
	wire := req.Clone(req.Context())
	encoded := compressed.Bytes()
	wire.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(encoded)), nil }
	wire.Body, _ = wire.GetBody()
	wire.ContentLength = int64(len(encoded))
	for name := range wire.Header {
		if strings.EqualFold(name, "Content-Length") {
			delete(wire.Header, name)
		}
	}
	wire.Header.Set("Content-Encoding", coding)
	resp, err := t.base.RoundTrip(wire)
	if err == nil && resp != nil && resp.StatusCode == http.StatusUnsupportedMediaType {
		// Keys are bounded by configured origins and the built-in Codex origin.
		preferences := resp.Header.Values("Accept-Encoding")
		explicitRejection := acceptsPlainInstead(preferences, coding)
		if t.cooldowns != nil && (explicitRejection || len(preferences) == 0) {
			t.cooldowns.Store(key, compressionRejection{req.URL.EscapedPath(), time.Now().Add(10 * time.Minute)})
		}
		slog.WarnContext(req.Context(), "upstream.request_compression_rejected", "origin", origin,
			"encoding", coding, "status", resp.StatusCode)
		// Return this rejection intact. Only a later, independently admitted
		// request may use the cooldown; a transport resend would bypass RPM,
		// authorization and upload-phase checks and hide this HTTP response.
	}
	return resp, err
}

var codingQuality = regexp.MustCompile(`(?i)^q=(0(?:\.[0-9]{0,3})?|1(?:\.0{0,3})?)$`)

// RFC 9110 section 12.5.3: a 415 with Accept-Encoding distinguishes an
// encoding rejection from an unsupported JSON media type. Fail closed on
// ambiguous preferences, including duplicate codings.
func acceptsPlainInstead(values []string, sent string) bool {
	if len(values) == 0 {
		return false
	}
	allowed := make(map[string]bool)
	for _, value := range values {
		for item := range strings.SplitSeq(value, ",") {
			if strings.TrimSpace(item) == "" {
				continue
			}
			name, weight, weighted := strings.Cut(item, ";")
			name = strings.ToLower(strings.TrimSpace(name))
			if !httpguts.ValidHeaderFieldName(name) {
				return false
			}
			if name == "x-gzip" {
				name = "gzip"
			}
			if _, exists := allowed[name]; exists {
				return false
			}
			enabled := true
			if weighted {
				weight = strings.TrimSpace(weight)
				if !codingQuality.MatchString(weight) {
					return false
				}
				enabled = strings.Trim(weight[2:], "0.") != ""
			}
			allowed[name] = enabled
		}
	}
	encoded, explicit := allowed[sent]
	if !explicit {
		encoded = allowed["*"]
	}
	plain, explicit := allowed["identity"]
	if !explicit {
		wildcard, exists := allowed["*"]
		plain = !exists || wildcard
	}
	return plain && !encoded
}

func (t *upstreamRequestCompressionTransport) coding(req *http.Request, origin string) string {
	for _, rule := range t.rules {
		if strings.EqualFold(rule.Origin, origin) {
			return rule.Encoding
		}
	}
	if strings.EqualFold(origin, "https://chatgpt.com") && req.URL.EscapedPath() == "/backend-api/codex/responses" &&
		service.IsOpenAICodexHTTPRequest(req.Context()) && !service.IsOpenAICodexStateProbeRequest(req.Context()) {
		if t.codexMode == "" {
			return "zstd"
		}
		return t.codexMode
	}
	return ""
}

func canCompressUpstreamRequest(req *http.Request) bool {
	// Active capture reads the original body as it is sent. Keep that path intact.
	if req.Method != http.MethodPost || req.Body == nil || req.Body == http.NoBody || req.GetBody == nil ||
		req.ContentLength < minUpstreamCompressionSize || req.ContentLength > maxUpstreamCompressionSize ||
		len(req.TransferEncoding) != 0 || len(req.Trailer) != 0 ||
		(req.Host != "" && !strings.EqualFold(req.Host, req.URL.Host)) ||
		requestcapture.FromContext(req.Context()) != nil || service.UpstreamCompressionRequiresPlainBody(req.Context()) ||
		strings.HasPrefix(req.Header.Get("Authorization"), "AWS4-") {
		return false
	}
	var contentTypes []string
	for name, values := range req.Header {
		switch strings.ToLower(name) {
		case "content-type":
			if len(contentTypes) > 0 || len(values) != 1 {
				return false
			}
			contentTypes = values
		case "content-encoding", "content-md5", "digest", "content-digest", "repr-digest", "signature", "signature-input", "x-amz-content-sha256":
			if len(values) > 0 {
				return false
			}
		}
	}
	if len(contentTypes) == 0 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	return err == nil && (mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"))
}

func compressUpstreamRequestBody(req *http.Request, coding string, dst io.Writer) bool {
	if req.Context().Err() != nil {
		return false
	}
	src, err := req.GetBody()
	if err != nil || src == nil {
		return false
	}
	defer func() { _ = src.Close() }()
	var encoder io.WriteCloser
	switch coding {
	case "gzip":
		encoder, err = gzip.NewWriterLevel(dst, gzip.BestSpeed)
	case "zstd":
		encoder, err = zstd.NewWriter(dst, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(1<<20))
	default:
		return false
	}
	if err != nil {
		return false
	}
	n, copyErr := io.Copy(encoder, io.LimitReader(upstreamCompressionReader{req.Context(), src}, req.ContentLength+1))
	closeErr := encoder.Close()
	return copyErr == nil && closeErr == nil && n == req.ContentLength && req.Context().Err() == nil
}

type upstreamCompressionReader struct {
	ctx context.Context
	src io.Reader
}

func (r upstreamCompressionReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.src.Read(p)
}
