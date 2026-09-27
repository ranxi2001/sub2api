package service

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

type excelBPSStreamEncoder interface {
	io.WriteCloser
	Flush() error
}

// The image is repeated by the Responses protocol. Keep every event intact and
// let a negotiated large-window encoder remove duplication on the wire. Flush
// after each SSE event so small text responses and keepalives stay incremental.
func startExcelBPSStreamCompression(c *gin.Context) (func() error, error) {
	original := c.Writer
	noop := func() error { return nil }
	if original.Written() || original.Header().Get("Content-Encoding") != "" {
		return noop, nil
	}
	encoding := excelBPSStreamEncoding(c.Request.Header.Values("Accept-Encoding"))
	if encoding == "" {
		return noop, nil
	}
	var encoder excelBPSStreamEncoder
	var err error
	switch encoding {
	case "zstd":
		encoder, err = zstd.NewWriter(original, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithWindowSize(8<<20))
	case "br":
		encoder = brotli.NewWriterOptions(original, brotli.WriterOptions{Quality: 4, LGWin: 23})
	case "gzip":
		encoder, err = gzip.NewWriterLevel(original, gzip.HuffmanOnly)
	}
	if err != nil {
		return nil, err
	}
	w := &excelBPSCompressionWriter{ResponseWriter: original, encoder: encoder}
	original.Header().Set("Content-Encoding", encoding)
	original.Header().Del("Content-Length")
	original.Header().Add("Vary", "Accept-Encoding")
	c.Writer = w
	return func() error {
		err := encoder.Close()
		if w.err != nil {
			err = w.err
		}
		c.Writer = original
		return err
	}, nil
}

// Only choose explicitly advertised encodings. Unknown or invalid entries keep
// the original uncompressed behavior, including clients without negotiation.
func excelBPSStreamEncoding(headers []string) string {
	qualities := make(map[string]float64)
	for _, header := range headers {
		for _, raw := range strings.Split(header, ",") {
			parts := strings.Split(raw, ";")
			name := strings.ToLower(strings.TrimSpace(parts[0]))
			quality := 1.0
			for _, parameter := range parts[1:] {
				key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
					quality = 0
					break
				}
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || !(parsed >= 0 && parsed <= 1) {
					quality = 0
					break
				}
				quality = parsed
			}
			if previous, exists := qualities[name]; !exists || quality < previous {
				qualities[name] = quality
			}
		}
	}
	selected, best := "", qualities["identity"]
	for _, name := range []string{"zstd", "br", "gzip"} {
		if q := qualities[name]; q > 0 && (q > best || (q == best && selected == "")) {
			selected, best = name, q
		}
	}
	return selected
}

type excelBPSCompressionWriter struct {
	gin.ResponseWriter
	encoder excelBPSStreamEncoder
	err     error
}

func (w *excelBPSCompressionWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.encoder.Write(p)
	w.err = err
	return n, err
}

func (w *excelBPSCompressionWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *excelBPSCompressionWriter) Flush() {
	if w.err == nil {
		w.err = w.encoder.Flush()
	}
	if w.err == nil {
		w.ResponseWriter.Flush()
	}
}

func (w *excelBPSCompressionWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
