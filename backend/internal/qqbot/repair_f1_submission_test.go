package qqbot

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func repairF1Reply(protocol string) string {
	if protocol == "responses" {
		return `{"status":"completed","output_text":"synthetic reply","usage":{"input_tokens":1,"output_tokens":1}}`
	}
	return `{"choices":[{"finish_reason":"stop","message":{"content":"synthetic reply"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
}

// Exercise the production siteChatTurn -> HTTP client path. The peer counts
// complete generation POST bodies, not reservations or eventual QQ messages.
func TestRepairF1SubmittedGenerationIsNotReplayed(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, failure := range []string{"headers_timeout", "attempt_deadline", "eof", "truncated_body"} {
			t.Run(protocol+"/"+failure, func(t *testing.T) {
				var posts atomic.Int32
				entered, release := make(chan struct{}), make(chan struct{})
				var unblock sync.Once
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil || r.Method != http.MethodPost || len(body) == 0 {
						t.Errorf("F1 fixture must receive a complete generation POST: %v", err)
						return
					}
					if posts.Add(1) > 1 {
						_, _ = io.WriteString(w, repairF1Reply(protocol))
						return
					}
					close(entered)
					if failure == "headers_timeout" || failure == "attempt_deadline" {
						<-release // The full POST was accepted before headers time out.
						return
					}
					hijacker, ok := w.(http.Hijacker)
					if !ok {
						t.Error("F1 fixture does not support hijacking")
						return
					}
					conn, _, err := hijacker.Hijack()
					if err != nil {
						t.Errorf("F1 fixture hijack: %v", err)
						return
					}
					defer func() { _ = conn.Close() }()
					if failure == "truncated_body" {
						_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{")
					}
				}))
				defer server.Close()
				defer unblock.Do(func() { close(release) })
				s := newMatureFixtureService(nil, nil, false, nil)
				transport := &http.Transport{ResponseHeaderTimeout: 150 * time.Millisecond}
				if failure == "attempt_deadline" {
					transport.ResponseHeaderTimeout = 0
				}
				defer transport.CloseIdleConnections()
				s.http = &http.Client{Transport: matureFixtureTransport{base: transport}, CheckRedirect: noRedirect}
				c := DefaultConfig()
				c.BaseURL, c.ChatProtocol = server.URL, protocol
				type outcome struct {
					turn chatTurnResult
					err  error
				}
				done := make(chan outcome, 1)
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				go func() {
					turn, err := s.siteChatTurn(ctx, c, []Message{{Role: "user", Content: "synthetic generation"}})
					done <- outcome{turn, err}
				}()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("F1 peer never accepted the complete POST")
				}
				var result outcome
				select {
				case result = <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("F1 call failed to return after the peer failure")
				}
				unblock.Do(func() { close(release) })
				require.EqualValues(t, 1, posts.Load(), "F1 accepted generation POST must not be replayed after uncertain response")
				require.Error(t, result.err)
				require.Equal(t, 1, result.turn.ChatExecution.Attempts)
				require.False(t, result.turn.ChatExecution.UsageComplete)
			})
		}
	}
}

func TestRepairF1DefinitelyUnsubmittedDialCanRetry(t *testing.T) {
	var posts, dials atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) == 0 {
			t.Errorf("F1 safe retry peer body: %v", err)
			return
		}
		posts.Add(1)
		_, _ = io.WriteString(w, repairF1Reply("chat_completions"))
	}))
	defer server.Close()
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if dials.Add(1) == 1 {
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	s := newMatureFixtureService(nil, nil, false, nil)
	s.http = &http.Client{Transport: matureFixtureTransport{base: transport}, CheckRedirect: noRedirect}
	c := DefaultConfig()
	c.BaseURL = server.URL
	turn, err := s.siteChatTurn(context.Background(), c, []Message{{Role: "user", Content: "synthetic safe retry"}})
	require.NoError(t, err)
	require.Equal(t, "synthetic reply", turn.Text)
	require.EqualValues(t, 2, dials.Load(), "F1 definitely unsubmitted connection may retry")
	require.EqualValues(t, 1, posts.Load(), "F1 safe retry submits exactly once")
}
