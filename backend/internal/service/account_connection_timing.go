package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
)

type accountTestStartKey struct{}

// WithAccountTestTimingStart includes handler validation in the test lifetime.
func WithAccountTestTimingStart(ctx context.Context, start time.Time) context.Context {
	return context.WithValue(ctx, accountTestStartKey{}, start)
}

// The adapter is request-scoped through the collector context. It never mutates
// the shared service or changes authentication, URLs, bodies, retries or billing.
type accountTimingUpstream struct{ upstream HTTPUpstream }

func timedAccountTestUpstream(upstream HTTPUpstream) HTTPUpstream {
	return accountTimingUpstream{upstream}
}

func (u accountTimingUpstream) Do(req *http.Request, proxy string, account int64, concurrency int) (*http.Response, error) {
	req, trace := requesttiming.StartAttempt(req, account, 0)
	resp, err := u.upstream.Do(req, proxy, account, concurrency)
	trace.Response(resp, err)
	recordOAuth429TestStatus(req, account, resp, err)
	accountTestResponseMode(req, resp)
	return resp, err
}

func (u accountTimingUpstream) DoWithTLS(req *http.Request, proxy string, account int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	req, trace := requesttiming.StartAttempt(req, account, 0)
	resp, err := u.upstream.DoWithTLS(req, proxy, account, concurrency, profile)
	trace.Response(resp, err)
	recordOAuth429TestStatus(req, account, resp, err)
	accountTestResponseMode(req, resp)
	return resp, err
}

func accountTestResponseMode(req *http.Request, resp *http.Response) {
	if resp == nil {
		return
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		requesttiming.Mode(req.Context(), "stream")
	} else {
		requesttiming.Mode(req.Context(), "non_stream")
	}
}

func accountTestStream(c *gin.Context) func() {
	requesttiming.Mode(c.Request.Context(), "stream")
	return func() { requesttiming.Mark(c.Request.Context(), "account_stream_end") }
}

func accountTestResponseContext(c *gin.Context, resp *http.Response) *gin.Context {
	c.Request = c.Request.WithContext(requesttiming.ResponseContext(c.Request.Context(), resp))
	return c
}

func accountTestContent(c *gin.Context, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	ctx := c.Request.Context()
	requesttiming.Mark(ctx, "account_first_content")
	requesttiming.Output(ctx, true, true, "")
	requesttiming.Stage(ctx, "sse_visible_first")
}

func beginAccountConnectionTiming(c *gin.Context) (*requesttiming.Collector, time.Time) {
	start, ok := c.Request.Context().Value(accountTestStartKey{}).(time.Time)
	if !ok {
		start = time.Now()
	}
	collector := requesttiming.New(start, 0)
	c.Request = c.Request.WithContext(requesttiming.With(c.Request.Context(), collector))
	return collector, start
}

func (s *AccountTestService) finishAccountConnectionTiming(c *gin.Context, collector *requesttiming.Collector, err error) {
	outcome := "success"
	var timeout net.Error
	switch {
	case errors.Is(c.Request.Context().Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		outcome = "timeout"
	case errors.Is(c.Request.Context().Err(), context.Canceled), errors.Is(err, context.Canceled):
		outcome = "canceled"
	case err != nil:
		outcome = "error"
	}
	requesttiming.Outcome(c.Request.Context(), outcome, outcome == "canceled")
	collector.WhenFinished(func(d requesttiming.Snapshot) {
		timing := requesttiming.AccountTestProjection(d)
		s.sendEvent(c, TestEvent{Type: "timing", Timing: &timing})
	})
	collector.Finish(c.Writer.Status(), outcome == "canceled")
}
