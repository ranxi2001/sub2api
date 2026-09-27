package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func TestExcelBPSCompressionNegotiation(t *testing.T) {
	for _, tc := range []struct{ header, want string }{
		{"", ""}, {"identity", ""}, {"*", ""}, {"deflate", ""},
		{"gzip, br, zstd", "zstd"}, {"gzip, br", "br"}, {"gzip", "gzip"},
		{"br;q=0,gzip;q=0", ""}, {"br;q=0.5,gzip;q=0.9", "gzip"},
		{"br;q=0,zstd;q=0.3", "zstd"}, {"BR; Q=1", "br"},
		{"br;q=NaN,gzip", "gzip"}, {"br;q=Infinity", ""}, {"br;q=2", ""},
		{"br;q=-1", ""}, {"br;q=bad", ""}, {"br;broken", ""},
		{"br,br;q=0,gzip", "gzip"}, {"identity;q=1,br;q=0.5", ""},
	} {
		t.Run(tc.header, func(t *testing.T) { require.Equal(t, tc.want, excelBPSStreamEncoding([]string{tc.header})) })
	}
	require.Equal(t, "br", excelBPSStreamEncoding([]string{"gzip;q=0.5", "br"}))
}

func excelBPSDecodeStream(t *testing.T, name string, source io.Reader) io.Reader {
	t.Helper()
	switch name {
	case "gzip":
		r, err := gzip.NewReader(source)
		require.NoError(t, err)
		t.Cleanup(func() { _ = r.Close() })
		return r
	case "br":
		return brotli.NewReader(source)
	case "zstd":
		r, err := zstd.NewReader(source, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(32<<20))
		require.NoError(t, err)
		t.Cleanup(r.Close)
		return r
	default:
		return source
	}
}

func TestExcelBPSCompressionDoesNotBufferFirstEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, name := range []string{"zstd", "br", "gzip"} {
		t.Run(name, func(t *testing.T) {
			first := "event: response.output_text.delta\ndata: {\"delta\":\"first\"}\n\n"
			last := "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"
			advance := make(chan struct{})
			errorsFromServer := make(chan error, 1)
			router := gin.New()
			router.GET("/", func(c *gin.Context) {
				finish, err := startExcelBPSStreamCompression(c)
				if err != nil {
					errorsFromServer <- err
					return
				}
				c.Header("Content-Type", "text/event-stream")
				_, err = c.Writer.WriteString(first)
				c.Writer.Flush()
				select {
				case <-advance:
				case <-c.Request.Context().Done():
				}
				if err == nil {
					_, err = c.Writer.WriteString(last)
				}
				if closeErr := finish(); err == nil {
					err = closeErr
				}
				errorsFromServer <- err
			})
			server := httptest.NewServer(router)
			defer server.Close()
			defer close(advance)
			client := &http.Client{Transport: &http.Transport{DisableCompression: true}, Timeout: 5 * time.Second}
			defer client.CloseIdleConnections()
			req, err := http.NewRequest(http.MethodGet, server.URL, nil)
			require.NoError(t, err)
			req.Header.Set("Accept-Encoding", name)
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, name, resp.Header.Get("Content-Encoding"))
			decoded := excelBPSDecodeStream(t, name, resp.Body)
			got := make([]byte, len(first))
			_, err = io.ReadFull(decoded, got)
			require.NoError(t, err, "first event must be readable while the server waits")
			require.Equal(t, first, string(got))
			advance <- struct{}{}
			rest, err := io.ReadAll(decoded)
			require.NoError(t, err)
			require.Equal(t, last, string(rest))
			require.NoError(t, <-errorsFromServer)
		})
	}
}

func TestExcelBPSCompressionGatewayContract(t *testing.T) {
	wire := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_compression\",\"status\":\"completed\",\"model\":\"gpt-5.6-sol\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n"
	var original []byte
	for _, name := range []string{"", "gzip", "br", "zstd"} {
		t.Run(name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
			svc := openAIClientToolsTestService(upstream)
			body := []byte("{\"model\":\"gpt-5.6-sol\",\"stream\":true,\"input\":\"test\"}")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			c.Request.Header.Set("Accept-Encoding", name)
			result, err := svc.Forward(context.Background(), c, excelAccount(), body)
			require.NoError(t, err)
			require.Equal(t, 10, result.Usage.InputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)
			require.Equal(t, "response.completed", result.UpstreamTerminalEvent)
			require.False(t, result.ClientDisconnect)
			require.Equal(t, name, rec.Header().Get("Content-Encoding"))
			decoded, err := io.ReadAll(excelBPSDecodeStream(t, name, bytes.NewReader(rec.Body.Bytes())))
			require.NoError(t, err)
			if name == "" {
				original = decoded
			} else {
				require.Equal(t, original, decoded)
			}
		})
	}
}

type excelBPSFailingWriter struct{ gin.ResponseWriter }

func (w excelBPSFailingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestExcelBPSCompressionErrorAndCommittedHeaders(t *testing.T) {
	for _, name := range []string{"br", "zstd", "gzip"} {
		t.Run(name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			c.Request.Header.Set("Accept-Encoding", name)
			original := excelBPSFailingWriter{c.Writer}
			c.Writer = original
			finish, err := startExcelBPSStreamCompression(c)
			require.NoError(t, err)
			_, _ = c.Writer.WriteString("data: value\n\n")
			c.Writer.Flush()
			require.True(t, errors.Is(finish(), io.ErrClosedPipe))
			require.Equal(t, original, c.Writer)
		})
	}
	for _, alreadyEncoded := range []bool{false, true} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Request.Header.Set("Accept-Encoding", "br")
		if alreadyEncoded {
			c.Header("Content-Encoding", "gzip")
		} else {
			c.Writer.Flush()
		}
		original := c.Writer
		finish, err := startExcelBPSStreamCompression(c)
		require.NoError(t, err)
		require.NoError(t, finish())
		require.Equal(t, original, c.Writer)
	}
}

// This opt-in replay measures an existing file delivery; it never calls a model.
func TestExcelBPSCompressionExistingImage(t *testing.T) {
	path := os.Getenv("BPS_COMPRESSION_FIXTURE")
	if path == "" {
		t.Skip("existing image fixture not requested")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var call map[string]any
	require.NoError(t, json.Unmarshal(raw, &call))
	input, ok := call["input"].(string)
	require.True(t, ok)
	payloads := []map[string]any{
		{"type": "response.custom_tool_call_input.delta", "delta": input},
		{"type": "response.custom_tool_call_input.done", "input": input},
		{"type": "response.output_item.done", "item": call},
		{"type": "response.completed", "response": map[string]any{"output": []any{call}}},
	}
	var events [][]byte
	var plain bytes.Buffer
	for _, payload := range payloads {
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		event := []byte("event: " + payload["type"].(string) + "\ndata: " + string(data) + "\n\n")
		events = append(events, event)
		plain.Write(event)
	}
	for _, name := range []string{"zstd", "br", "gzip"} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			c.Request.Header.Set("Accept-Encoding", name)
			start := time.Now()
			finish, err := startExcelBPSStreamCompression(c)
			require.NoError(t, err)
			for _, event := range events {
				_, err = c.Writer.Write(event)
				require.NoError(t, err)
				c.Writer.Flush()
			}
			require.NoError(t, finish())
			elapsed := time.Since(start)
			decoded, err := io.ReadAll(excelBPSDecodeStream(t, name, bytes.NewReader(rec.Body.Bytes())))
			require.NoError(t, err)
			require.Equal(t, plain.Bytes(), decoded)
			ratio := float64(rec.Body.Len()) / float64(plain.Len())
			t.Logf("encoding=%s plain_bytes=%d wire_bytes=%d saved_pct=%.2f encode_ms=%.2f", name, plain.Len(), rec.Body.Len(), 100*(1-ratio), float64(elapsed)/float64(time.Millisecond))
			if name != "gzip" {
				require.Less(t, ratio, 0.30)
			}
		})
	}
}
