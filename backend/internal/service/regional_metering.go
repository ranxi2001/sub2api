package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/regional"
	"github.com/Wei-Shaw/sub2api/internal/regional/ledger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// RegionalMeteringAdmission resolves a durable identity, immutable pricing and
// all budget reservations for the authenticated key/account. Implementations
// must never accept identity from public request headers or async snapshots.
type RegionalMeteringAdmission func(context.Context, *APIKey, *Account, []byte) (ledger.Reservation, ledger.Price, error)
type RegionalMetering struct {
	Ledger    ledger.RequestLedger
	Admit     RegionalMeteringAdmission
	Readiness func(context.Context) error
	Run       func(context.Context)
	Stop      func()
}

func (m *RegionalMetering) Check(ctx context.Context) error {
	if m == nil || m.Ledger == nil || m.Admit == nil || m.Readiness == nil {
		return errors.New("funds runtime not configured")
	}
	return m.Readiness(ctx)
}
func (s *OpenAIGatewayService) RegionalFundsReadiness() *RegionalMetering {
	if s == nil {
		return nil
	}
	return s.regionalMetering
}

func WithRegionalMetering(m *RegionalMetering) OpenAIGatewayOption {
	return func(s *OpenAIGatewayService) { s.regionalMetering = m }
}

type regionalMeterKey struct{}
type regionalMeterAttempt struct {
	ledger           ledger.RequestLedger
	reservation      ledger.Reservation
	price            ledger.Price
	output           int64
	fullOutputWindow bool
	sent             atomic.Bool
	finalUsage       atomic.Pointer[OpenAIUsage]
}
type regionalMeterSessionKey struct{}
type regionalMeterSession struct {
	current atomic.Pointer[regionalMeterAttempt]
}

func regionalMeterFinishForward(ctx context.Context, result *OpenAIForwardResult, forwardErr error, finish func(*OpenAIForwardResult, error) error) error {
	finishErr := finish(result, forwardErr)
	if regionalMeterAttemptFrom(ctx) == nil {
		return forwardErr
	}
	if forwardErr != nil || finishErr != nil {
		return errors.New("regional metered request unresolved; automatic retry refused")
	}
	return nil
}

// A metered turn has one billable upstream submission. Unknown failures cannot
// be retried under a fresh identity, even if no downstream bytes were produced.
func regionalMeterAttemptFrom(ctx context.Context) *regionalMeterAttempt {
	attempt, _ := ctx.Value(regionalMeterKey{}).(*regionalMeterAttempt)
	if attempt == nil {
		if session, ok := ctx.Value(regionalMeterSessionKey{}).(*regionalMeterSession); ok {
			attempt = session.current.Load()
		}
	}
	return attempt
}

func regionalMeterObserveUsage(ctx context.Context, raw []byte, terminal string) {
	attempt := regionalMeterAttemptFrom(ctx)
	if attempt == nil {
		return
	}
	if terminal != "json" && terminal != "response.completed" && terminal != "response.done" {
		return
	}
	usage := gjson.GetBytes(raw, "response.usage")
	if !usage.Exists() {
		usage = gjson.GetBytes(raw, "usage")
	}
	if !usage.IsObject() {
		return
	}
	in := usage.Get("input_tokens")
	out := usage.Get("output_tokens")
	if !in.Exists() {
		in = usage.Get("prompt_tokens")
	}
	if !out.Exists() {
		out = usage.Get("completion_tokens")
	}
	if in.Type != gjson.Number || out.Type != gjson.Number {
		return
	}
	input, inputErr := strconv.ParseInt(in.Raw, 10, 32)
	output, outputErr := strconv.ParseInt(out.Raw, 10, 32)
	if inputErr != nil || outputErr != nil || input < 0 || output < 0 {
		return
	}
	for _, path := range []string{"input_tokens_details.audio_tokens", "prompt_tokens_details.audio_tokens", "output_tokens_details.audio_tokens", "completion_tokens_details.audio_tokens"} {
		if usage.Get(path).Int() != 0 {
			return
		}
	}
	measured, ok := openAIUsageFromGJSON(usage)
	if !ok {
		return
	}
	if measured.InputTokens != int(input) || measured.OutputTokens != int(output) {
		return
	}
	attempt.finalUsage.Store(&measured)
}

func regionalMeterValidatePayload(ctx context.Context, raw []byte) error {
	a := regionalMeterAttemptFrom(ctx)
	if a == nil {
		return nil
	}
	model := gjson.GetBytes(raw, "model").String()
	limit := gjson.GetBytes(raw, "max_output_tokens")
	if !limit.Exists() {
		limit = gjson.GetBytes(raw, "max_tokens")
	}
	fullWindow := a.fullOutputWindow && a.output == a.price.MaxOutput && !limit.Exists()
	if model != a.price.Model || (!fullWindow && (limit.Type != gjson.Number || limit.Int() <= 0 || limit.Int() > a.output)) {
		return errors.New("outbound model/output limit changed; regional request refused")
	}
	for _, field := range []string{"tools", "previous_response_id", "conversation", "service_tier"} {
		if v := gjson.GetBytes(raw, field); v.Exists() && v.Raw != "null" && v.Raw != "[]" {
			return errors.New("outbound feature has no regional cost bound")
		}
	}
	return nil
}

func regionalMeterHTTPRequest(req *http.Request) error {
	if regionalMeterAttemptFrom(req.Context()) == nil {
		return nil
	}
	if req.Body == nil {
		return errors.New("missing bounded request body")
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, 1<<20+1))
	_ = req.Body.Close()
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("bounded request too large")
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))
	return regionalMeterValidatePayload(req.Context(), raw)
}

func regionalMeterBeforeSend(ctx context.Context) error {
	attempt := regionalMeterAttemptFrom(ctx)
	if attempt == nil {
		return nil
	}
	if !attempt.sent.CompareAndSwap(false, true) {
		return errors.New("metered request cannot replay upstream")
	}
	return attempt.ledger.Start(ctx, attempt.reservation.RequestID)
}

func regionalTextOutputBound(body []byte) (string, int64, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return "", 0, err
	}
	// All fields capable of implicit tools, media, server-side continuation,
	// tier changes or transformed requests are denied until separately bounded.
	allowed := map[string]bool{"model": true, "input": true, "messages": true, "instructions": true, "max_output_tokens": true, "max_tokens": true, "stream": true, "type": true, "store": true}
	for field := range fields {
		if !allowed[field] {
			return "", 0, errors.New("request feature has no regional cost bound")
		}
	}
	var model string
	_ = json.Unmarshal(fields["model"], &model)
	var output int64
	if raw, ok := fields["max_output_tokens"]; ok {
		if err := json.Unmarshal(raw, &output); err != nil {
			return "", 0, err
		}
	} else {
		if err := json.Unmarshal(fields["max_tokens"], &output); err != nil {
			return "", 0, err
		}
	}
	if model == "" || output <= 0 {
		return "", 0, errors.New("explicit model and output limit required")
	}
	for _, field := range []string{"input", "instructions"} {
		if raw, ok := fields[field]; ok {
			var text string
			if err := json.Unmarshal(raw, &text); err != nil {
				return "", 0, errors.New("only text input and instructions are bounded")
			}
		}
	}
	if raw, ok := fields["type"]; ok {
		var kind string
		if err := json.Unmarshal(raw, &kind); err != nil || kind != "response.create" {
			return "", 0, errors.New("only response.create has a regional cost bound")
		}
	}
	if raw, ok := fields["messages"]; ok {
		var messages []struct {
			Role    string
			Content string
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&messages); err != nil || len(messages) == 0 {
			return "", 0, errors.New("only text messages are bounded")
		}
		for _, m := range messages {
			if m.Role != "system" && m.Role != "user" && m.Role != "assistant" {
				return "", 0, errors.New("unbounded tool message")
			}
		}
	}
	return model, output, nil
}

func (s *OpenAIGatewayService) beginRegionalMeter(ctx context.Context, c *gin.Context, account *Account, body []byte) (context.Context, func(*OpenAIForwardResult, error) error, error) {
	noop := func(*OpenAIForwardResult, error) error { return nil }
	if attempt := regionalMeterAttemptFrom(ctx); attempt != nil {
		ctx = context.WithValue(ctx, regionalMeterKey{}, attempt)
		return ctx, noop, nil
	}
	if s.regionalMetering == nil {
		if s.cfg != nil && s.cfg.Runtime.RegionalAuthFile != "" {
			return ctx, nil, errors.New("regional metering unavailable")
		}
		return ctx, noop, nil
	}
	m := s.regionalMetering
	if m.Ledger == nil || m.Admit == nil {
		return ctx, nil, errors.New("regional metering dependencies unavailable")
	}
	key := getAPIKeyFromContext(c)
	if key == nil || account == nil {
		return ctx, nil, errors.New("authenticated metering identity required")
	}
	// Other adapters have automatic tools or sends outside the native boundary.
	// They need separate certified bounds before metering can accept them.
	if account.Platform != PlatformOpenAI || account.IsOpenAIPassthroughEnabled() || account.IsOpenAIAgentIdentity() || account.IsOpenAIPersonalAccessToken() || account.IsCopilotSDKEnabled() || accountHasPrismBrowser(account) || (s.pluginManager != nil && s.pluginManager.ShouldRouteOpenAIOAuth(account)) {
		return ctx, nil, errors.New("adapter does not expose a bounded regional send")
	}
	model, output, err := regionalTextOutputBound(body)
	if err != nil {
		return ctx, nil, err
	}
	if account.IsExcelBPSEnabledForModel(model) && s.excelBPSGloballyEnabled(ctx) {
		return ctx, nil, errors.New("BPS adapter has no certified regional cost bound")
	}
	r, p, err := m.Admit(ctx, key, account, body)
	if err != nil {
		return ctx, nil, err
	}
	if r.UserID != key.UserID || r.APIKeyID != key.ID || r.AccountID != account.ID || p.Model != model {
		return ctx, nil, ledger.ErrConflict
	}
	if account.Type == AccountTypeOAuth {
		output = p.MaxOutput
	}
	maximum, err := p.Maximum(output)
	if err != nil {
		return ctx, nil, err
	}
	digest := sha256.Sum256(body)
	r.Model = model
	r.PricingVersion = p.Version()
	r.Maximum = maximum
	r.PayloadSHA256 = hex.EncodeToString(digest[:])
	fresh, err := m.Ledger.Reserve(ctx, r)
	if err != nil {
		return ctx, nil, err
	}
	if !fresh {
		return ctx, nil, ledger.ErrAlreadyDispatched
	}
	attempt := &regionalMeterAttempt{ledger: m.Ledger, reservation: r, price: p, output: output, fullOutputWindow: account.Type == AccountTypeOAuth}
	ctx = context.WithValue(ctx, regionalMeterKey{}, attempt)
	finish := func(result *OpenAIForwardResult, forwardErr error) error {
		if result != nil {
			result.RegionalMetered = true
		}
		if !attempt.sent.Load() {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			return m.Ledger.CancelBeforeStart(cleanup, r.RequestID)
		}
		// Existing HTTP adapters do not all expose a terminal-usage flag. On an
		// interrupted turn retain the hold rather than trusting partial counters.
		measured := attempt.finalUsage.Load()
		if forwardErr != nil || result == nil || result.UsageUnavailable || measured == nil {
			return errors.New("regional final usage unavailable; hold retained")
		}
		measuredTokens := *measured
		// Cache presence is protection metadata, not a billable token counter.
		measuredTokens.CacheReadKnown = result.Usage.CacheReadKnown
		measuredTokens.cacheReadMediaObserved = result.Usage.cacheReadMediaObserved
		if result.Usage != measuredTokens {
			return errors.New("regional usage differs from terminal evidence")
		}
		if result.ImageCount != 0 || result.VideoCount != 0 || result.Usage.ImageInputTokens != 0 || result.Usage.ImageOutputTokens != 0 || result.Usage.OutputTokens > int(output) {
			return errors.New("regional usage outside text cost bounds")
		}
		if result.UpstreamResponseModelConflict || result.UpstreamModel != "" && result.UpstreamModel != model || result.ServiceTier != nil && *result.ServiceTier != "" && *result.ServiceTier != "default" {
			return errors.New("regional pricing identity changed")
		}
		u := result.Usage
		input := int64(u.InputTokens) - int64(u.CacheCreationInputTokens) - int64(u.CacheReadInputTokens)
		charge, err := p.Charge(input, int64(u.OutputTokens), int64(u.CacheCreationInputTokens), int64(u.CacheReadInputTokens))
		if err != nil {
			return err
		}
		completed := time.Now().UTC()
		if completed.Before(r.AdmittedAt) {
			completed = r.AdmittedAt
		}
		event := ledger.Event{Usage: regional.UsageEvent{SchemaVersion: regional.SchemaVersion, EventID: r.RequestID, LeaseID: r.LeaseID, RegionID: r.RegionID, UserID: r.UserID, APIKeyID: r.APIKeyID, AccountID: r.AccountID, AdmittedAt: r.AdmittedAt, CompletedAt: completed, Charge: charge}, Model: r.Model, PricingVersion: r.PricingVersion, PayloadSHA256: r.PayloadSHA256, InputTokens: input, OutputTokens: int64(u.OutputTokens), CacheCreationTokens: int64(u.CacheCreationInputTokens), CacheReadTokens: int64(u.CacheReadInputTokens)}
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return m.Ledger.Complete(settle, event)
	}
	return ctx, finish, nil
}

// wrapRegionalWSTurns installs the same durable boundary on every real ingress
// turn. A lost/failed turn remains held; a replay of the same turn is rejected.
func (s *OpenAIGatewayService) wrapRegionalWSTurns(ctx context.Context, c *gin.Context, account *Account, first []byte, hooks *OpenAIWSIngressHooks) (context.Context, *OpenAIWSIngressHooks, func(), error) {
	if s.regionalMetering == nil {
		if s.cfg != nil && s.cfg.Runtime.RegionalAuthFile != "" {
			return ctx, nil, nil, errors.New("regional metering unavailable")
		}
		return ctx, hooks, func() {}, nil
	}
	session := &regionalMeterSession{}
	var mu sync.Mutex
	finishes := map[int]func(*OpenAIForwardResult, error) error{}
	seen := map[int]bool{}
	prepare := func(turn int, payload []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if seen[turn] || len(finishes) > 0 {
			return ledger.ErrAlreadyDispatched
		}
		turnCtx, finish, err := s.beginRegionalMeter(ctx, c, account, payload)
		if err != nil {
			return err
		}
		attempt, _ := turnCtx.Value(regionalMeterKey{}).(*regionalMeterAttempt)
		if attempt == nil {
			return errors.New("missing turn meter")
		}
		finishes[turn] = finish
		seen[turn] = true
		session.current.Store(attempt)
		return nil
	}
	if err := prepare(1, first); err != nil {
		return ctx, nil, nil, err
	}
	wrapped := OpenAIWSIngressHooks{}
	if hooks != nil {
		wrapped = *hooks
	}
	before := wrapped.BeforeRequest
	after := wrapped.AfterTurn
	wrapped.BeforeRequest = func(turn int, payload []byte, model string) error {
		if before != nil {
			if err := before(turn, payload, model); err != nil {
				return err
			}
		}
		if turn == 1 {
			return nil
		}
		return prepare(turn, payload)
	}
	wrapped.AfterTurn = func(turn int, result *OpenAIForwardResult, turnErr error) {
		mu.Lock()
		finish := finishes[turn]
		delete(finishes, turn)
		mu.Unlock()
		if finish != nil {
			turnErr = errors.Join(turnErr, finish(result, turnErr))
		}
		if after != nil {
			after(turn, result, turnErr)
		}
	}
	cleanup := func() {
		mu.Lock()
		defer mu.Unlock()
		for turn, finish := range finishes {
			_ = finish(nil, errors.New("websocket ended without final usage"))
			delete(finishes, turn)
		}
	}
	return context.WithValue(ctx, regionalMeterSessionKey{}, session), &wrapped, cleanup, nil
}
