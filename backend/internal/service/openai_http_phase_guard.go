package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

type openAIHTTPBudgetKey struct{}

const openAIHTTPBudgetGinKey = "openai_http_phase_budget"

// This state belongs to the inbound request, not to a selected account. All
// Forward calls on the same Gin context therefore consume the same deadlines.
type openAIHTTPBudget struct {
	mu             sync.Mutex
	started        time.Time
	preOutputLimit time.Duration
	uploadDeadline time.Time
	progress       chan struct{}
	progressOnce   sync.Once
}

func (s *OpenAIGatewayService) withOpenAIHTTPBudget(ctx context.Context, c *gin.Context) context.Context {
	if budget, ok := ctx.Value(openAIHTTPBudgetKey{}).(*openAIHTTPBudget); ok && budget != nil {
		return ctx
	}
	var budget *openAIHTTPBudget
	if c != nil {
		if value, ok := c.Get(openAIHTTPBudgetGinKey); ok {
			budget, _ = value.(*openAIHTTPBudget)
		}
	}
	if budget == nil {
		budget = &openAIHTTPBudget{started: time.Now(), progress: make(chan struct{})}
		if s != nil && s.cfg != nil {
			budget.preOutputLimit = time.Duration(s.cfg.Gateway.OpenAIPreOutputBudgetSeconds) * time.Second
		}
		if c != nil {
			c.Set(openAIHTTPBudgetGinKey, budget)
		}
	}
	return context.WithValue(ctx, openAIHTTPBudgetKey{}, budget)
}

func observeOpenAIHTTPProgress(ctx context.Context) {
	if budget, ok := ctx.Value(openAIHTTPBudgetKey{}).(*openAIHTTPBudget); ok && budget != nil {
		budget.progressOnce.Do(func() { close(budget.progress) })
	}
}

func (s *OpenAIGatewayService) openAIUploadTimeout(account *Account) time.Duration {
	seconds := 0
	if s != nil && s.cfg != nil {
		seconds = s.cfg.Gateway.OpenAIUploadTimeoutSeconds
	}
	if account != nil {
		// An explicit zero disables the global upload limit for this account.
		switch value := account.Extra["openai_upload_timeout_seconds"].(type) {
		case float64:
			if value >= 0 && value <= 1800 && value == float64(int(value)) {
				seconds = int(value)
			}
		case int:
			if value >= 0 && value <= 1800 {
				seconds = value
			}
		}
	}
	return time.Duration(seconds) * time.Second
}

type openAIHTTPPhaseTimeout struct {
	phase string
	cause error
}

func (e *openAIHTTPPhaseTimeout) Error() string {
	if e.phase == "response_header_timeout" {
		return "OpenAI upstream response header timeout; request may still complete upstream; automatic replay disabled"
	}
	return "OpenAI upstream " + e.phase + " deadline exceeded"
}
func (e *openAIHTTPPhaseTimeout) Unwrap() error {
	if e.cause != nil {
		return e.cause
	}
	return context.DeadlineExceeded
}

// ResponseHeaderTimeout starts after upload. A timeout at that point cannot
// prove upstream stopped executing. Track writes without adding another timer,
// and reuse the terminal phase-error path across every HTTP gateway adapter.
// The transport's explicit header-timeout diagnostic is also a veto when a
// custom transport does not emit standard trace callbacks.
func guardOpenAIResponseHeaderTimeout(req *http.Request, account *Account) (*http.Request, func(*http.Response, error) error) {
	if account == nil || account.Platform != PlatformOpenAI {
		return req, func(_ *http.Response, err error) error { return err }
	}
	var submitted atomic.Bool
	trace := &httptrace.ClientTrace{
		WroteHeaders: func() { submitted.Store(true) },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				submitted.Store(true)
			}
		},
	}
	req = req.Clone(httptrace.WithClientTrace(req.Context(), trace))
	return req, func(resp *http.Response, err error) error {
		var phaseErr *openAIHTTPPhaseTimeout
		if err == nil || resp != nil || errors.As(err, &phaseErr) || errors.Is(err, context.Canceled) {
			return err
		}
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() &&
			(submitted.Load() || strings.Contains(err.Error(), "timeout awaiting response headers")) {
			return &openAIHTTPPhaseTimeout{phase: "response_header_timeout", cause: err}
		}
		return err
	}
}

type openAIHTTPPhaseGuard struct {
	ctx        context.Context
	cancel     context.CancelCauseFunc
	uploaded   chan struct{}
	stop       chan struct{}
	done       chan struct{}
	uploadOnce sync.Once
	stopOnce   sync.Once
}

// The guard is installed after upstream cancellation detachment, immediately
// around the actual egress path. It also covers physical TLS/proxy retries.
func (s *OpenAIGatewayService) guardOpenAIHTTPRequest(req *http.Request, account *Account) (*http.Request, *openAIHTTPPhaseGuard) {
	budget, _ := req.Context().Value(openAIHTTPBudgetKey{}).(*openAIHTTPBudget)
	if budget == nil || account == nil || account.Platform != PlatformOpenAI || isControlledExperiment(req.Context()) {
		return req, nil
	}
	uploadLimit := s.openAIUploadTimeout(account)
	budget.mu.Lock()
	if uploadLimit > 0 {
		candidate := time.Now().Add(uploadLimit)
		if budget.uploadDeadline.IsZero() || candidate.Before(budget.uploadDeadline) {
			budget.uploadDeadline = candidate
		}
	}
	uploadDeadline := budget.uploadDeadline
	budget.mu.Unlock()
	if uploadLimit <= 0 {
		uploadDeadline = time.Time{}
	}
	var totalDeadline time.Time
	if budget.preOutputLimit > 0 {
		select {
		case <-budget.progress:
		default:
			totalDeadline = budget.started.Add(budget.preOutputLimit)
		}
	}
	if uploadDeadline.IsZero() && totalDeadline.IsZero() {
		return req, nil
	}
	ctx, cancel := context.WithCancelCause(req.Context())
	g := &openAIHTTPPhaseGuard{ctx: ctx, cancel: cancel, uploaded: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{})}
	// Reject an already expired budget before another upstream submission.
	if (!uploadDeadline.IsZero() && !time.Now().Before(uploadDeadline)) || (!totalDeadline.IsZero() && !time.Now().Before(totalDeadline)) {
		phase := "pre_output_budget"
		if !uploadDeadline.IsZero() && !time.Now().Before(uploadDeadline) {
			phase = "upload_timeout"
		}
		cancel(&openAIHTTPPhaseTimeout{phase: phase})
	}
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			g.uploadComplete()
		}
	}}
	go g.watch(uploadDeadline, totalDeadline, budget.progress)
	return req.Clone(httptrace.WithClientTrace(ctx, trace)), g
}

func (g *openAIHTTPPhaseGuard) uploadComplete() { g.uploadOnce.Do(func() { close(g.uploaded) }) }

func (g *openAIHTTPPhaseGuard) watch(uploadDeadline, totalDeadline time.Time, progress <-chan struct{}) {
	defer close(g.done)
	newTimer := func(deadline time.Time) (*time.Timer, <-chan time.Time) {
		if deadline.IsZero() {
			return nil, nil
		}
		timer := time.NewTimer(time.Until(deadline))
		return timer, timer.C
	}
	uploadTimer, uploadCh := newTimer(uploadDeadline)
	totalTimer, totalCh := newTimer(totalDeadline)
	if uploadTimer != nil {
		defer uploadTimer.Stop()
	}
	if totalTimer != nil {
		defer totalTimer.Stop()
	}
	uploaded := g.uploaded
	for uploadCh != nil || totalCh != nil {
		select {
		case <-g.stop:
			return
		case <-g.ctx.Done():
			return
		case <-uploaded:
			if uploadTimer != nil {
				uploadTimer.Stop()
			}
			uploadCh, uploaded = nil, nil
		case <-progress:
			if totalTimer != nil {
				totalTimer.Stop()
			}
			totalCh, progress = nil, nil
		case <-uploadCh:
			select {
			case <-g.uploaded:
				uploadCh, uploaded = nil, nil
				continue
			default:
			}
			g.cancel(&openAIHTTPPhaseTimeout{phase: "upload_timeout"})
			return
		case <-totalCh:
			select {
			case <-progress:
				totalCh, progress = nil, nil
				continue
			default:
			}
			g.cancel(&openAIHTTPPhaseTimeout{phase: "pre_output_budget"})
			return
		}
	}
}

func (g *openAIHTTPPhaseGuard) close() {
	if g == nil {
		return
	}
	g.stopOnce.Do(func() { close(g.stop); <-g.done; g.cancel(context.Canceled) })
}

func (g *openAIHTTPPhaseGuard) finish(resp *http.Response, err error) (*http.Response, error) {
	if g == nil {
		return resp, err
	}
	if resp != nil {
		g.uploadComplete()
	} // Early response headers also end upload waiting.
	var phaseErr *openAIHTTPPhaseTimeout
	if errors.As(context.Cause(g.ctx), &phaseErr) {
		g.close()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, phaseErr
	}
	if err != nil || resp == nil || resp.Body == nil {
		g.close()
		return resp, err
	}
	resp.Body = &openAIHTTPPhaseBody{ReadCloser: resp.Body, guard: g}
	return resp, nil
}

type openAIHTTPPhaseBody struct {
	io.ReadCloser
	guard *openAIHTTPPhaseGuard
}

func (b *openAIHTTPPhaseBody) Close() error {
	b.guard.close()
	return b.ReadCloser.Close()
}

func openAIHTTPPhaseError(resp *http.Response) error {
	if resp == nil || resp.Request == nil {
		return nil
	}
	var err *openAIHTTPPhaseTimeout
	if errors.As(context.Cause(resp.Request.Context()), &err) {
		return err
	}
	return nil
}

// Upload/pre-output deadlines never authorize replay. A response-header timeout
// after the request was written is replayable only because the caller explicitly
// bills a 1:1 retry surcharge for the first attempt's possible upstream cost.
// If the response is already committed, even that path stays terminal.
func handleOpenAIHTTPPhaseError(c *gin.Context, account *Account, err error) error {
	var phaseErr *openAIHTTPPhaseTimeout
	if !errors.As(err, &phaseErr) {
		return err
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: account.Platform, AccountID: account.ID,
		UpstreamStatusCode: http.StatusGatewayTimeout, Kind: phaseErr.phase, Message: phaseErr.Error()})
	setOpsUpstreamError(c, http.StatusGatewayTimeout, phaseErr.Error(), "")
	if c == nil {
		return err
	}
	StopOpenAICompactSSEKeepaliveCommitted(c)
	if phaseErr.phase == "response_header_timeout" && !IsResponseCommitted(c) {
		written := c.Writer.Written()
		if written {
			MarkOpsStreamFailure(c, phaseErr.phase, phaseErr.phase, phaseErr.Error(), http.StatusGatewayTimeout)
		}
		// Replay is deliberately allowed here: the caller has chosen to pay the
		// first attempt's possible upstream cost and charge it back 1:1.
		const replayMessage = "OpenAI upstream response header timeout; retrying another account (first attempt may be billed)"
		return &UpstreamFailoverError{
			StatusCode:               http.StatusGatewayTimeout,
			ResponseBody:             []byte(replayMessage),
			Stage:                    GatewayFailureStageInference,
			Scope:                    GatewayFailureScopeRequest,
			NextAccountAction:        NextAccountRetry,
			ClientStatusCode:         http.StatusGatewayTimeout,
			ClientMessage:            replayMessage,
			HeaderTimeoutSubmitted:   true,
			SafeToFailoverAfterWrite: written,
		}
	}
	if c.Writer.Written() {
		MarkOpsStreamFailure(c, phaseErr.phase, phaseErr.phase, phaseErr.Error(), http.StatusGatewayTimeout)
	}
	if !IsResponseCommitted(c) {
		if c.Writer.Written() {
			if strings.Contains(c.Writer.Header().Get("Content-Type"), "text/event-stream") {
				writeOpenAICompactSSEFailureMessage(c, http.StatusGatewayTimeout, phaseErr.phase, phaseErr.Error())
			}
		} else {
			c.JSON(http.StatusGatewayTimeout, gin.H{"error": gin.H{"type": phaseErr.phase, "message": phaseErr.Error()}})
		}
		MarkResponseCommitted(c)
	}
	return err
}
