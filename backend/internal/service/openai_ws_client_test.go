package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestCoderOpenAIWSClientDialer_ProxyHTTPClientReuse(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	c1, err := impl.proxyHTTPClient("http://127.0.0.1:8080")
	require.NoError(t, err)
	c2, err := impl.proxyHTTPClient("http://127.0.0.1:8080")
	require.NoError(t, err)
	require.Same(t, c1, c2, "同一代理地址应复用同一个 HTTP 客户端")

	c3, err := impl.proxyHTTPClient("http://127.0.0.1:8081")
	require.NoError(t, err)
	require.NotSame(t, c1, c3, "不同代理地址应分离客户端")
}

func TestCoderOpenAIWSClientDialer_ProxyHTTPClientInvalidURL(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	_, err := impl.proxyHTTPClient("://bad")
	require.Error(t, err)
}

func TestCoderOpenAIWSClientDialer_TransportMetricsSnapshot(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	_, err := impl.proxyHTTPClient("http://127.0.0.1:18080")
	require.NoError(t, err)
	_, err = impl.proxyHTTPClient("http://127.0.0.1:18080")
	require.NoError(t, err)
	_, err = impl.proxyHTTPClient("http://127.0.0.1:18081")
	require.NoError(t, err)

	snapshot := impl.SnapshotTransportMetrics()
	require.Equal(t, int64(1), snapshot.ProxyClientCacheHits)
	require.Equal(t, int64(2), snapshot.ProxyClientCacheMisses)
	require.InDelta(t, 1.0/3.0, snapshot.TransportReuseRatio, 0.0001)
}

func TestCoderOpenAIWSClientDialer_ProxyClientCacheCapacity(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	total := openAIWSProxyClientCacheMaxEntries + 32
	for i := 0; i < total; i++ {
		_, err := impl.proxyHTTPClient(fmt.Sprintf("http://127.0.0.1:%d", 20000+i))
		require.NoError(t, err)
	}

	impl.proxyMu.Lock()
	cacheSize := len(impl.proxyClients)
	impl.proxyMu.Unlock()

	require.LessOrEqual(t, cacheSize, openAIWSProxyClientCacheMaxEntries, "代理客户端缓存应受容量上限约束")
}

func TestCoderOpenAIWSClientDialer_ProxyClientCacheIdleTTL(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	oldProxy := "http://127.0.0.1:28080"
	_, err := impl.proxyHTTPClient(oldProxy)
	require.NoError(t, err)

	impl.proxyMu.Lock()
	oldEntry := impl.proxyClients[oldProxy]
	require.NotNil(t, oldEntry)
	oldEntry.lastUsedUnixNano = time.Now().Add(-openAIWSProxyClientCacheIdleTTL - time.Minute).UnixNano()
	impl.proxyMu.Unlock()

	// 触发一次新的代理获取，驱动 TTL 清理。
	_, err = impl.proxyHTTPClient("http://127.0.0.1:28081")
	require.NoError(t, err)

	impl.proxyMu.Lock()
	_, exists := impl.proxyClients[oldProxy]
	impl.proxyMu.Unlock()

	require.False(t, exists, "超过空闲 TTL 的代理客户端应被回收")
}

func TestCoderOpenAIWSClientDialer_ProxyTransportTLSHandshakeTimeout(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	client, err := impl.proxyHTTPClient("http://127.0.0.1:38080")
	require.NoError(t, err)
	require.NotNil(t, client)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport)
	require.Equal(t, 10*time.Second, transport.TLSHandshakeTimeout)
}

func TestCoderOpenAIWSClientConn_DoesNotSupportIdlePingWithoutReader(t *testing.T) {
	require.False(t, (&coderOpenAIWSClientConn{}).SupportsIdlePingWithoutReader())
}

func TestCoderOpenAIWSClientConn_PreparedJSONOwnershipAndRawValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode coderws.CompressionMode
	}{
		{"uncompressed", coderws.CompressionDisabled},
		{"context_takeover", coderws.CompressionContextTakeover},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			readGate := make(chan struct{})
			type capture struct {
				payloads [][]byte
				err      error
			}
			received := make(chan capture, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var got capture
				defer func() { received <- got }()
				conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: tc.mode})
				if err != nil {
					got.err = err
					return
				}
				defer func() { _ = conn.CloseNow() }()
				conn.SetReadLimit(1 << 20)
				// Keep the socket open without consuming messages until the caller
				// has overwritten its buffers and checked invalid RawMessage.
				select {
				case <-readGate:
				case <-ctx.Done():
					got.err = ctx.Err()
					return
				}
				for {
					kind, p, err := conn.Read(ctx)
					if err != nil {
						if coderws.CloseStatus(err) != coderws.StatusNormalClosure {
							got.err = err
						}
						return
					}
					if kind != coderws.MessageText {
						got.err = fmt.Errorf("expected text frame, got %v", kind)
						return
					}
					got.payloads = append(got.payloads, p)
				}
			}))
			defer func() {
				cancel()
				srv.Close()
			}()
			conn, resp, err := coderws.Dial(ctx, srv.URL, &coderws.DialOptions{CompressionMode: tc.mode})
			require.NoError(t, err)
			defer func() { _ = conn.CloseNow() }()
			if tc.mode == coderws.CompressionContextTakeover {
				require.Contains(t, resp.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate")
				require.NotContains(t, resp.Header.Get("Sec-WebSocket-Extensions"), "no_context_takeover")
			}
			client := &coderOpenAIWSClientConn{conn: conn}

			var buf bytes.Buffer
			var first []byte
			var expected [][]byte
			for _, text := range []string{"a", "b"} {
				buf.Reset()
				value := map[string]any{"type": "response.create", "input": strings.Repeat(text, 8<<10) + "<>&你好\u2028"}
				require.NoError(t, json.NewEncoder(&buf).Encode(value))
				p := buf.Bytes()
				if first == nil {
					first = p
				} else {
					require.Same(t, &first[0], &p[0], "the next encode must overwrite the same backing array")
				}
				expected = append(expected, bytes.Clone(p))
				require.NoError(t, client.WriteJSON(ctx, openAIWSPreparedJSON(p)))
			}
			clear(buf.Bytes()) // Also invalidate the second write before the peer reads.

			err = client.WriteJSON(ctx, json.RawMessage(`{"invalid":`))
			var syntaxErr *json.SyntaxError
			require.ErrorAs(t, err, &syntaxErr, "a closed socket is not evidence of JSON validation")
			var marshalErr *json.MarshalerError
			require.ErrorAs(t, err, &marshalErr)
			const barrier = `{"type":"test.barrier"}`
			require.NoError(t, client.WriteJSON(ctx, json.RawMessage(barrier)), "validation must leave the socket usable")
			expected = append(expected, []byte(barrier+"\n"))

			close(readGate)
			// Drain through an ordered close, rather than a sleep or a racy channel
			// length check, so any extra application message is included below.
			require.NoError(t, conn.Close(coderws.StatusNormalClosure, "test complete"))
			select {
			case got := <-received:
				require.NoError(t, got.err)
				require.Equal(t, expected, got.payloads, "prepared bytes must survive reuse; invalid JSON must never be sent")
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}
