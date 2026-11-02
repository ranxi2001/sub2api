package service

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWrapSafetyPromptResponseStreamsBeforeEOF(t *testing.T) {
	for _, contentType := range []string{"text/event-stream", "", "application/octet-stream", "application/json"} {
		t.Run("content_type="+contentType, func(t *testing.T) {
			gateway, store := newSafetyPromptTestGateway(t, true, true, 30)
			source, writer := io.Pipe()
			resp := gateway.wrapSafetyPromptResponse(&http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {contentType}},
				Body:       source,
			}, &safetyPromptRequestInfo{Marker: "sp-test", Protocol: safetyPromptProtocolResponses})
			t.Cleanup(func() {
				_ = writer.Close()
				_ = resp.Body.Close()
			})

			first := sseFrame("response.created", `{"type":"response.created"}`) +
				sseFrame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"hello"}`)
			written := make(chan error, 1)
			go func() {
				// Fragment even the field name; keep the upstream open after the first text.
				for i := range first {
					if _, err := writer.Write([]byte{first[i]}); err != nil {
						written <- err
						return
					}
				}
				written <- nil
			}()
			type readResult struct {
				text string
				err  error
			}
			read := make(chan readResult, 1)
			go func() {
				buf := make([]byte, len(first))
				_, err := io.ReadFull(resp.Body, buf)
				read <- readResult{string(buf), err}
			}()
			select {
			case result := <-read:
				require.NoError(t, result.err)
				require.Equal(t, first, result.text)
			case <-time.After(time.Second):
				t.Fatal("first SSE events were buffered until upstream EOF")
			}
			require.NoError(t, <-written)
			require.NoError(t, writer.Close())
			rest, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Empty(t, rest)
			require.Empty(t, store.List(time.Time{}, time.Time{}, "", 30))
		})
	}
}

func TestSafetyPromptResponseDetectionPreservesPrefix(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		sse  bool
	}{
		{"event", "event: response.created\n\ndata: {}\n\n", true},
		{"data", "data: {}\n\n", true},
		{"comment", ": heartbeat\n\n", true},
		{"id", "id: 1\ndata: {}\n\n", true},
		{"retry", "retry: 1000\n\n", true},
		{"bom", "\xef\xbb\xbfevent: response.created\ndata: {}\n\n", true},
		{"blank_lines", "\r\n\r\ndata: {}\r\n\r\n", true},
		{"json", " \n{\"output\":[]}", false},
		{"empty", "", false},
		{"short", "ev", false},
		{"html", "<html></html>", false},
		{"bounded", strings.Repeat("\n", safetyPromptMaxResponsePeek) + "data: {}\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// One-byte reads also cover prefixes split across upstream reads.
			reader := bufio.NewReader(iotest.OneByteReader(strings.NewReader(tc.body)))
			require.Equal(t, tc.sse, safetyPromptLooksLikeSSE(reader))
			output, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.Equal(t, tc.body, string(output))
		})
	}
}

func TestWrapSafetyPromptResponseDetectsFormatAndRewritesSignal(t *testing.T) {
	marker := "sp-test"
	response := `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"sp-test"}]}]}`
	for _, contentType := range []string{"", "application/json", "application/octet-stream"} {
		for _, format := range []string{"json", "sse"} {
			t.Run(contentType+"/"+format, func(t *testing.T) {
				gateway, store := newSafetyPromptTestGateway(t, true, true, 30)
				input := " \n" + response
				if format == "sse" {
					input = ": heartbeat\n\n" +
						sseFrame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"sp-"}`) +
						sseFrame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"test"}`) +
						sseFrame("response.completed", `{"type":"response.completed","response":`+response+`}`)
				}
				resp := gateway.wrapSafetyPromptResponse(&http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {contentType}, "Content-Length": {"123"}},
					Body:       io.NopCloser(iotest.OneByteReader(strings.NewReader(input))),
				}, &safetyPromptRequestInfo{Marker: marker, Protocol: safetyPromptProtocolResponses, RetentionDays: 30})
				defer func() { _ = resp.Body.Close() }()
				output, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				payload := output
				path := "output.0.content.0.text"
				if format == "sse" {
					require.True(t, strings.HasPrefix(string(output), ": heartbeat\n\n"))
					payload = gjsonFramePayload(t, string(output), "response.completed")
					path = "response." + path
				}
				require.Equal(t, safetyPromptRefusal+marker, safetyPromptTestString(payload, path))
				require.Len(t, store.List(time.Time{}, time.Time{}, "", 30), 1)
				require.Equal(t, int64(-1), resp.ContentLength)
				require.Empty(t, resp.Header.Get("Content-Length"))
			})
		}
	}
}

func TestWrapSafetyPromptResponsePreservesNonSSEBody(t *testing.T) {
	for _, input := range []string{"", "ev", "null", `{"output":[]}`, strings.Repeat("x", safetyPromptMaxJSONResponse+2)} {
		gateway, store := newSafetyPromptTestGateway(t, true, true, 30)
		resp := gateway.wrapSafetyPromptResponse(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(input)),
		}, &safetyPromptRequestInfo{Marker: "sp-test", Protocol: safetyPromptProtocolResponses})
		output, err := io.ReadAll(resp.Body)
		require.NoError(t, resp.Body.Close())
		require.NoError(t, err)
		require.Equal(t, input, string(output))
		require.Empty(t, store.List(time.Time{}, time.Time{}, "", 30))
	}
}

func TestWrapSafetyPromptResponsePreservesSSEReadError(t *testing.T) {
	gateway, _ := newSafetyPromptTestGateway(t, true, true, 30)
	wantErr := errors.New("upstream interrupted")
	frame := sseFrame("response.created", `{"type":"response.created"}`)
	resp := gateway.wrapSafetyPromptResponse(&http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(io.MultiReader(strings.NewReader(frame), iotest.ErrReader(wantErr))),
	}, &safetyPromptRequestInfo{Marker: "sp-test"})
	defer func() { _ = resp.Body.Close() }()
	output, err := io.ReadAll(resp.Body)
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, frame, string(output))
}

func TestWrapSafetyPromptResponseCloseUnblocksRead(t *testing.T) {
	for _, prefix := range []string{"ev", "{", "data:"} {
		t.Run(prefix, func(t *testing.T) {
			gateway, _ := newSafetyPromptTestGateway(t, true, true, 30)
			source, writer := io.Pipe()
			resp := gateway.wrapSafetyPromptResponse(&http.Response{
				StatusCode: http.StatusOK,
				Body:       source,
			}, &safetyPromptRequestInfo{Marker: "sp-test"})
			t.Cleanup(func() {
				_ = writer.Close()
				_ = resp.Body.Close()
			})
			read := make(chan struct{})
			go func() {
				_, _ = io.Copy(io.Discard, resp.Body)
				close(read)
			}()
			_, err := writer.Write([]byte(prefix))
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.NoError(t, resp.Body.Close())
			select {
			case <-read:
			case <-time.After(time.Second):
				t.Fatal("Close did not unblock the response reader")
			}
			_, err = writer.Write([]byte("x"))
			require.ErrorIs(t, err, io.ErrClosedPipe, "Close must reach the upstream body")
		})
	}
}
