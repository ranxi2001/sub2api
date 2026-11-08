package service

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type openAIWSAgentRecoveryKey struct{}
type openAIWSAgentRecoveryRetryKey struct{}
type openAIWSAgentSourceKey struct{}

// The scope uses immutable client input before account projection. The original
// identity callbacks, fresh authorization and per-turn admission keep their order.
func (s *OpenAIGatewayService) WithOpenAIWSAgentRecovery(ctx context.Context, c *gin.Context, body []byte, session, model string) context.Context {
	ctx = s.WithOpenAIAgentMessageRecovery(ctx, c, body, session, model)
	return context.WithValue(ctx, openAIWSAgentRecoveryRetryKey{}, &atomic.Bool{})
}

func OpenAIWSAgentRecoveryCanRetry(ctx context.Context) bool {
	blocked, _ := ctx.Value(openAIWSAgentRecoveryRetryKey{}).(*atomic.Bool)
	return !openAIAgentRecoveryBound(ctx) && (blocked == nil || !blocked.Load())
}

func OpenAIWSAgentRecoveryBound(ctx context.Context) bool { return openAIAgentRecoveryBound(ctx) }

// One manager per forwarding attempt serializes an immediate next create with
// the previous terminal write/commit. Passthrough's AfterTurn runs BEFORE that
// write and must never grant ownership. No plaintext or frame is retained here.
type openAIWSAgentRecovery struct {
	mu       sync.Mutex
	service  *OpenAIGatewayService
	c        *gin.Context
	ctx      context.Context
	session  string
	request  *openAIAgentRecoveryRequest
	finish   func(bool)
	blocked  *atomic.Bool
	itemDone [][32]byte
	known    map[[32]byte]bool
}

func newOpenAIWSAgentRecovery(s *OpenAIGatewayService, ctx context.Context, c *gin.Context) *openAIWSAgentRecovery {
	r := openAIAgentRecoveryFromContext(ctx)
	if r == nil {
		return nil
	}
	blocked, _ := ctx.Value(openAIWSAgentRecoveryRetryKey{}).(*atomic.Bool)
	known := make(map[[32]byte]bool)
	for key := range r.known {
		known[key] = true
	}
	return &openAIWSAgentRecovery{service: s, ctx: ctx, c: c, session: r.session, blocked: blocked, known: known}
}

func openAIWSAgentRecoveryFromContext(ctx context.Context) *openAIWSAgentRecovery {
	r, _ := ctx.Value(openAIWSAgentRecoveryKey{}).(*openAIWSAgentRecovery)
	return r
}

func (r *openAIWSAgentRecovery) abort() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finish != nil {
		r.finish(false)
		r.finish = nil
	}
}

func (r *openAIWSAgentRecovery) preventReplay() {
	if r.blocked != nil {
		r.blocked.Store(true)
	}
}

// req captures the actual URL, final authorization headers and outbound model.
// A fragment namespaces the fingerprint by adapter; this copy is never sent.
// Thus a WS grant cannot authorize an HTTP or different-adapter continuation.
func (r *openAIWSAgentRecovery) begin(account *Account, source, payload []byte, req *http.Request, mode string) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finish != nil {
		r.finish(false)
		r.finish = nil
	}
	r.itemDone = nil
	model := gjson.GetBytes(payload, "model").String()
	// Lookup scope follows the channel's forward model, while the wire digest
	// below includes the final account-mapped model.
	if forward, ok := openAIForwardModelFromContext(r.c.Request.Context()); ok && forward.model != "" {
		model = forward.model
	} else if initial := openAIAgentRecoveryFromContext(r.ctx); initial != nil {
		model = initial.model
	}
	ctx := r.service.WithOpenAIAgentMessageRecovery(r.ctx, r.c, source, r.session, model)
	r.request = openAIAgentRecoveryFromContext(ctx)
	for _, key := range r.request.keys {
		if r.known[key] && !r.request.known[key] {
			return openAIAgentRecoveryError("owner_unavailable")
		}
	}
	if err := r.service.checkOpenAIAgentRecoveryDispatch(ctx, r.c, account, payload); err != nil {
		return err
	}
	r.request.disallowLearning = r.request.disallowLearning || account.IsExcelBPSEnabledForModel(model) || account.IsPrismBrowserEnabledForModel(model)
	if err := r.service.checkOpenAIAgentRecoveryAccount(ctx, account); err != nil {
		return err
	}
	previous := gjson.GetBytes(source, "previous_response_id").String()
	if r.request.owner != nil && previous != "" {
		owner, err := r.service.getOpenAIWSStateStore().GetResponseAccount(ctx, r.request.groupID, previous)
		if err != nil || owner != account.ID || gjson.GetBytes(payload, "previous_response_id").String() != previous {
			return openAIAgentRecoveryError("owner_unavailable")
		}
	}
	copyReq := req.Clone(ctx)
	copyURL := *req.URL
	copyURL.Fragment = "agent-recovery-" + mode
	copyReq.URL = &copyURL
	// The actual request may have already been consumed by a caller. Only the
	// immutable final payload is used for the compatibility check.
	bodyReq, err := http.NewRequestWithContext(ctx, http.MethodPost, copyURL.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	copyReq.GetBody = bodyReq.GetBody
	finish, err := r.service.prepareOpenAIAgentRecoveryAttempt(ctx, account, copyReq)
	if err != nil {
		return err
	}
	r.finish = finish
	if r.request.owner != nil || len(r.request.items) > 0 {
		r.preventReplay()
	}
	return nil
}

func (r *openAIWSAgentRecovery) beginSocket(account *Account, source, payload []byte, address string, headers http.Header, mode string) error {
	if r == nil {
		return nil
	}
	req, err := http.NewRequest(http.MethodPost, address, nil)
	if err != nil {
		return err
	}
	req.Header = headers
	return r.begin(account, source, payload, req, mode)
}

func (r *openAIWSAgentRecovery) write(payload []byte, write func() error) error {
	if r == nil {
		return write()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	request := r.request
	if request == nil || request.delivery == nil || r.finish == nil {
		return write()
	}
	ctx := context.WithValue(r.ctx, openAIAgentRecoveryContextKey{}, request)
	observer := &gin.Context{Request: (&http.Request{}).WithContext(ctx)}
	if err := observeOpenAIAgentRecoveryOutput(observer, payload, true); err != nil {
		r.preventReplay()
		return err
	}
	event := gjson.GetBytes(payload, "type").String()
	if event == "response.output_item.done" {
		r.itemDone = append(r.itemDone, openAIAgentRecoveryDigests(payload, "item")...)
	}
	if len(request.delivery.keys) > 0 {
		r.preventReplay()
	}
	err := write()
	if err != nil || r.ctx.Err() != nil {
		r.finish(false)
		r.finish = nil
		return err
	}
	if isOpenAIWSTerminalEvent(event) && r.finish != nil {
		completed := (event == "response.completed" || event == "response.done") && openAIAgentCompletedResponse(payload, true)
		if completed && len(gjson.GetBytes(payload, "response.output").Array()) == 0 {
			// Some WS providers emit complete items separately and an empty
			// terminal output. Only item.done followed by completed qualifies.
			request.outputs = append(request.outputs, r.itemDone...)
		}
		if completed && (len(request.outputs) > 0 || len(request.keys) > 0) {
			responseID := gjson.GetBytes(payload, "response.id").String()
			storeCtx, cancel := context.WithTimeout(r.ctx, 3*time.Second)
			if responseID == "" || r.service.getOpenAIWSStateStore().BindResponseAccount(storeCtx, request.groupID, responseID, request.delivery.binding.accountID, r.service.openAIWSResponseStickyTTL()) != nil {
				completed = false
			}
			cancel()
		}
		r.finish(completed)
		if completed {
			for _, key := range request.keys {
				r.known[key] = true
			}
			for _, item := range request.outputs {
				r.known[request.itemKey(item)] = true
			}
		}
		r.finish = nil
	}
	return nil
}

func openAIWSAgentRecoveryClose(err error) error {
	return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "agent task continuation is unavailable; original task was preserved", err)
}
