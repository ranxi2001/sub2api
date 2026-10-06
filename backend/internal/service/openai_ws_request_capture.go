package service

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/requestcapture"
	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
)

type captureUpstreamFrameConn struct {
	openaiwsv2.FrameConn
	capture *requestcapture.Session
	account int64
}

func (c *captureUpstreamFrameConn) WriteFrame(ctx context.Context, t coderws.MessageType, b []byte) error {
	n := c.capture.WSRequest(c.account, b)
	err := c.FrameConn.WriteFrame(ctx, t, b)
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
	err := conn.Write(ctx, t, payload)
	capture := requestcapture.FromContext(ctx)
	if err == nil {
		capture.Frame("client_response", 0, payload)
	} else {
		capture.MarkPartial("client_write_failed")
		capture.MarkError("client_write_failed")
	}
	return err
}
func captureWSLeaseWrite(ctx context.Context, account int64, headers http.Header, value any, write func() error) error {
	capture := requestcapture.FromContext(ctx)
	if payload, ok := value.(openAIWSPreparedJSON); ok {
		value = json.RawMessage(payload)
	}
	n := capture.WSRequest(account, value)
	err := write()
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
