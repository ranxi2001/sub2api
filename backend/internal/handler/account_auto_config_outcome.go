package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
	"time"
)

func autoConfigSuccessfulFrame(frame []byte) bool {
	_, data := parseOpsSSEFrameEnvelope(frame)
	return service.IsSuccessfulStreamTerminal(data)
}
func observeAutoConfigRequest(c *gin.Context, ops *service.OpsService, w *opsCaptureWriter, started time.Time) {
	if ops == nil || c.Request.Method != http.MethodPost || isCountTokensRequest(c) {
		return
	}
	path := c.Request.URL.Path
	if !strings.HasSuffix(path, "/responses") && !strings.HasSuffix(path, "/messages") && !strings.HasSuffix(path, "/chat/completions") && !strings.Contains(path, ":generateContent") && !strings.Contains(path, ":streamGenerateContent") {
		return
	}
	failed := map[int64]bool{}
	failureCategories := map[int64]string{}
	if v, ok := c.Get(service.OpsUpstreamErrorsKey); ok {
		if events, ok := v.([]*service.OpsUpstreamErrorEvent); ok {
			for _, e := range events {
				if e != nil && e.AccountID > 0 {
					failed[e.AccountID] = true
					if e.Kind == "transport_error" {
						failureCategories[e.AccountID] = e.Kind
					}
				}
			}
		}
	}
	for id := range failed {
		if service.IsPrismBrowserAttempt(c, id) {
			continue
		}
		ops.ObserveConcurrencyResult(service.AccountConcurrencyResult{AccountID: id, StartedAt: started, FailureCategory: failureCategories[id]})
	}
	// Earlier attempts still report their own failures. The final request-size
	// rejection says nothing about the selected account's concurrency capacity.
	if isOpsRequestSizeRejection(c) {
		return
	}
	id := c.GetInt64(opsAccountIDKey)
	if id <= 0 || failed[id] || service.IsPrismBrowserAttempt(c, id) {
		return
	}
	if c.Request.Context().Err() != nil || c.Writer.Status() == 499 {
		return
	}
	state, _ := w.lockActive()
	if state == nil {
		return
	}
	terminalError, completed := state.terminalFound, state.terminalSuccess || service.OpsStreamCompletedForAccount(c, id)
	state.mu.RUnlock()
	success := c.Writer.Status() >= 200 && c.Writer.Status() < 300 && len(c.Errors) == 0 && !terminalError && len(service.GetOpsStreamErrors(c)) == 0
	streaming := c.GetBool(opsStreamKey) || strings.Contains(c.Writer.Header().Get("Content-Type"), "text/event-stream")
	if streaming && !completed {
		success = false
	}
	ops.ObserveConcurrencyResult(service.AccountConcurrencyResult{AccountID: id, StartedAt: started, Success: success})
}
