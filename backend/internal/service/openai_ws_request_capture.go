package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/Wei-Shaw/sub2api/internal/requestcapture"
	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	"github.com/Wei-Shaw/sub2api/internal/util/transportdiag"
	coderws "github.com/coder/websocket"
)

type captureUpstreamFrameConn struct {
	openaiwsv2.FrameConn
	capture *requestcapture.Session
	account int64
	route   transportdiag.Route
}

func (c *captureUpstreamFrameConn) WriteFrame(ctx context.Context, t coderws.MessageType, b []byte) error {
	finish := requesttiming.StartRoute(ctx, c.account, "websocket_write", c.route)
	n := c.capture.WSRequestWithRoute(c.account, b, c.route)
	err := c.FrameConn.WriteFrame(ctx, t, b)
	finish(0, err)
	c.capture.AttemptResponse(n, 101, nil, err)
	return err
}
func (c *captureUpstreamFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	t, b, err := c.FrameConn.ReadFrame(ctx)
	if len(b) > 0 {
		c.capture.Frame("upstream_response", c.capture.LastAttempt(), b)
	}
	if err != nil && ctx.Err() == nil && coderws.CloseStatus(err) != coderws.StatusNormalClosure && coderws.CloseStatus(err) != coderws.StatusGoingAway {
		c.capture.MarkPartial("upstream_websocket_closed")
		c.capture.MarkError("upstream_websocket_closed")
	}
	return t, b, err
}

// WriteCapturedWSClient observes successfully delivered downstream frames.
func WriteCapturedWSClient(ctx context.Context, conn *coderws.Conn, t coderws.MessageType, payload []byte) error {
	capture := requestcapture.FromContext(ctx)
	return capture.DeliverClientFrame(payload, func() error { return conn.Write(ctx, t, payload) })
}
func captureWSLeaseWrite(ctx context.Context, account int64, headers http.Header, value any, write func() error) error {
	capture := requestcapture.FromContext(ctx)
	if payload, ok := value.(openAIWSPreparedJSON); ok {
		value = json.RawMessage(payload)
	}
	route := openAIWSCaptureRoute(ctx, "")
	finish := requesttiming.StartRoute(ctx, account, "websocket_write", route)
	n := capture.WSRequestWithRoute(account, value, route)
	err := write()
	finish(0, err)
	capture.AttemptResponse(n, 101, headers, err)
	return err
}
func captureWSLeaseRead(ctx context.Context, read func() ([]byte, error)) ([]byte, error) {
	b, err := read()
	capture := requestcapture.FromContext(ctx)
	if len(b) > 0 {
		capture.Frame("upstream_response", capture.LastAttempt(), b)
	}
	if err != nil && ctx.Err() == nil && coderws.CloseStatus(err) != coderws.StatusNormalClosure && coderws.CloseStatus(err) != coderws.StatusGoingAway {
		capture.MarkPartial("upstream_websocket_read_failed")
		capture.MarkError("upstream_websocket_read_failed")
	}
	return b, err
}

// Handshakes are separate from response.create attempts, including successful
// handshakes. Reused connections have frame attempts only. Background prewarming
// has no request collector and remains unrecorded.
func captureOpenAIWSHandshake(ctx context.Context, account int64, wsURL string) func(int, http.Header, error) {
	if requestcapture.FromContext(ctx) == nil && requesttiming.From(ctx) == nil {
		return func(int, http.Header, error) {}
	}
	capture := requestcapture.FromContext(ctx)
	route := openAIWSCaptureRoute(ctx, wsURL)
	finishTiming := requesttiming.StartRoute(ctx, account, "websocket_handshake", route)
	n := capture.BeginHandshake(account, route)
	return func(status int, headers http.Header, err error) {
		capture.AttemptResponse(n, status, headers, err)
		finishTiming(status, err)
	}
}

func captureOpenAIWSHandshakeBody(ctx context.Context, body []byte, headers http.Header) {
	capture := requestcapture.FromContext(ctx)
	if capture == nil || len(body) == 0 {
		return
	}
	st := capture.NewStream("upstream_response", capture.LastAttempt(), 0, headers.Get("Content-Type"), headers)
	_, _ = st.Write(body)
	_ = st.Close()
}

func openAIWSCaptureRoute(ctx context.Context, wsURL string) transportdiag.Route {
	route := transportdiag.RouteFromContext(ctx)
	route.Upstream = "websocket"
	// Pooled writes have no URL argument; the pool serves Responses exclusively.
	route.Endpoint = "responses"
	if wsURL != "" {
		if u, err := url.Parse(wsURL); err == nil {
			route.Endpoint = transportdiag.EndpointCategory(u.Path)
		} else {
			route.Endpoint = "other"
		}
	}
	return route
}
