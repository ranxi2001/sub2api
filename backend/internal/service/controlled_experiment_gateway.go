package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

type controlledExperimentGateway struct {
	accounts AccountRepository
	gateway  *OpenAIGatewayService
	tests    *AccountTestService
	billing  *BillingService
}

func NewControlledExperimentGateway(accounts AccountRepository, gateway *OpenAIGatewayService, tests *AccountTestService, billing *BillingService) ControlledExperimentExecutor {
	return &controlledExperimentGateway{accounts: accounts, gateway: gateway, tests: tests, billing: billing}
}

func controlledFrozenRouteMatches(account *Account, route ControlledRoute, model string) bool {
	return account != nil && account.ID == route.AccountID && reflect.DeepEqual(account.ParentAccountID, route.ParentAccountID) &&
		reflect.DeepEqual(account.ProxyID, route.ProxyID) && account.GetMappedModel(model) == route.MappedModel
}

func (g *controlledExperimentGateway) Preflight(ctx context.Context, route ControlledRoute, spec ControlledExperimentSpec) ControlledPreflight {
	p := ControlledPreflight{Catalog: "unknown"}
	account, err := g.accounts.GetByID(ctx, route.AccountID)
	if err != nil || account == nil {
		p.Reason = "account_unavailable"
		return p
	}
	if !controlledFrozenRouteMatches(account, route, spec.Model) {
		p.Reason = "frozen_route_changed"
		return p
	}
	if p.Reason = g.gateway.controlledRouteReason(ctx, account, spec.Model, route.Channel); p.Reason != "" {
		return p
	}
	p.identityReference, err = g.identitySnapshot(ctx, account)
	if err != nil {
		p.Reason = "parent_identity_unavailable"
		return p
	}
	body, _ := controlledPayload(spec, ControlledTask{Prompt: "eligibility"}, nil)
	if route.Channel == "native_http" || route.Channel == "native_ws" {
		if shouldForwardOpenAIResponsesViaChatCompletions(account, body) {
			p.Reason = "chat_conversion_not_supported"
			return p
		}
	}
	// Catalog reads do not generate answers and do not prove entitlement.
	// A catalog failure is recorded, and the budgeted generation probe decides.
	if g.tests != nil {
		models, err := g.tests.FetchOpenAIAccountModels(ctx, account)
		if err == nil {
			p.Catalog = "not_listed"
			for _, m := range models {
				if m.ID == spec.Model {
					p.Catalog = "listed"
					break
				}
			}
		}
	}
	p.Available = true
	p.Reason = "configuration_available"
	return p
}

type controlledRecorder struct {
	*httptest.ResponseRecorder
	mu       sync.Mutex
	cancel   context.CancelFunc
	overflow bool
}

func (r *controlledRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Body.Len()+len(p) > 4<<20 {
		r.overflow = true
		r.cancel()
		return 0, errors.New("experiment response capture limit")
	}
	return r.ResponseRecorder.Write(p)
}

func (r *controlledRecorder) Flush() { r.mu.Lock(); defer r.mu.Unlock(); r.ResponseRecorder.Flush() }

func (g *controlledExperimentGateway) Execute(parent context.Context, route ControlledRoute, spec ControlledExperimentSpec, body []byte, session string) (*ControlledTurn, ControlledDiagnostic) {
	started := time.Now()
	d := ControlledDiagnostic{Code: "account_unavailable", CostIncomplete: true, OutputTypes: []string{}}
	account, err := g.accounts.GetByID(parent, route.AccountID)
	if err != nil || account == nil {
		return nil, d
	}
	if !controlledFrozenRouteMatches(account, route, spec.Model) {
		d.Code = "frozen_route_changed"
		return nil, d
	}
	identity, err := g.identitySnapshot(parent, account)
	if err != nil || route.identityReference != nil && !reflect.DeepEqual(route.identityReference, identity) {
		d.Code = "account_identity_changed_since_preflight"
		return nil, d
	}
	mode := &controlledExperimentMode{channel: route.Channel}
	if route.Channel == "prism" {
		id, err := uuid.Parse(session)
		if err != nil {
			d.Code = "invalid_experiment_session"
			return nil, d
		}
		// The adapter requires 64 lowercase hex characters. Random experiment
		// identities have no relationship to any business user's API key.
		identity := strings.ReplaceAll(id.String(), "-", "")
		mode.prismIdentity = identity + identity
		mode.prismTurnID = strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
	}
	ctx := context.WithValue(parent, controlledExperimentContextKey{}, mode)
	ctx = withPelicanTestOptions(ctx, pelicanTestOptions{observeOnly: true})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	recorder := &controlledRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Session-Id", session)
	// The capture is internal. This marker selects the same WS forwarding path
	// as WS ingress, while all account/global WS gates still apply.
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	if route.Channel == "native_ws" {
		SetOpenAIClientTransport(c, OpenAIClientTransportWS)
	}
	result, forwardErr := g.gateway.Forward(ctx, c, account, body)
	d.DurationMs = time.Since(started).Milliseconds()
	d.Submissions = int(mode.submissions.Load())
	d.ActualChannel = mode.actualChannel
	d.HTTPStatus = recorder.Code
	d.UpstreamEndpoint = GetActualOpenAIUpstreamEndpoint(c)
	d.ForwardingStatus = c.GetInt(OpsUpstreamStatusCodeKey)
	var failover *UpstreamFailoverError
	if errors.As(forwardErr, &failover) {
		d.ForwardingStatus = failover.StatusCode
	}
	d.UpstreamStatus = int(mode.httpStatus.Load())
	if d.UpstreamStatus == 0 && d.Submissions == 0 {
		d.UpstreamStatus = d.ForwardingStatus
	}
	if result != nil {
		d.RequestID = result.RequestID
		d.ResponseID = result.ResponseID
		d.UpstreamModel = result.UpstreamModel
		if d.UpstreamModel == "" {
			d.UpstreamModel = route.MappedModel
		}
		d.ResponseModel = result.UpstreamResponseModel
		d.Usage = result.Usage
		d.UsageSource = "provider"
		if result.UsageUnavailable {
			d.UsageSource = "unavailable"
		}
		if result.ReasoningEffort != nil {
			d.EffectiveEffort = *result.ReasoningEffort
		}
		d.EffortEvidence = "request_sent"
		if g.billing != nil && !result.UsageUnavailable {
			cacheCreation := result.Usage.CacheCreationInputTokens
			if excelBPSCacheCreationBilledAsInput(account, result) {
				cacheCreation = 0
			}
			inputTokens := max(result.Usage.InputTokens-result.Usage.CacheReadInputTokens-cacheCreation, 0)
			cost, err := g.billing.CalculateCost(d.UpstreamModel, UsageTokens{InputTokens: inputTokens, OutputTokens: result.Usage.OutputTokens, CacheReadTokens: result.Usage.CacheReadInputTokens, CacheCreationTokens: cacheCreation}, 1)
			if err == nil && cost != nil {
				d.CostUSD = &cost.TotalCost
				d.CostIncomplete = d.UsageSource != "provider"
			}
		}
	}
	postCtx, postCancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
	fresh, lookupErr := g.accounts.GetByID(postCtx, route.AccountID)
	freshIdentity, identityErr := g.identitySnapshot(postCtx, fresh)
	postCancel()
	d.IdentityStable = lookupErr == nil && identityErr == nil && controlledFrozenRouteMatches(fresh, route, spec.Model) && reflect.DeepEqual(identity, freshIdentity)
	response, model, effort, terminal, parseErr := parseControlledResponse(recorder.Body.Bytes())
	d.Terminal = terminal
	if d.ResponseModel == "" {
		d.ResponseModel = model
	}
	effortMismatch := effort != "" && d.EffectiveEffort != "" && effort != d.EffectiveEffort
	if effort != "" {
		d.EffectiveEffort = effort
		d.EffortEvidence = "upstream_declared"
		// Prism synthesizes Responses metadata from its request. BPS is an
		// adapter boundary too; neither echo proves the provider's actual effort.
		if route.Channel == "prism" || route.Channel == "bps" {
			d.EffortEvidence = "adapter_declared"
		}
	}
	if response != nil {
		if !response.usagePresent {
			d.CostUSD = nil
			d.CostIncomplete = true
			d.UsageSource = "unavailable"
		}
		for _, raw := range response.Output {
			d.OutputTypes = append(d.OutputTypes, gjson.GetBytes(raw, "type").String())
		}
	}
	if d.ResponseModel == "" || d.ResponseModel != d.UpstreamModel {
		d.CostIncomplete = true
	}
	switch {
	case recorder.overflow:
		d.Code = "capture_limit"
	case ctx.Err() != nil:
		d.Code = "timeout_unknown"
	case forwardErr != nil && d.UpstreamStatus >= 400:
		d.Code = "upstream_rejected"
	case forwardErr != nil && d.Submissions > 0:
		d.Code = "transport_unknown"
	case forwardErr != nil:
		d.Code = "local_forwarding_rejected"
	case result == nil || result.ClientDisconnect:
		d.Code = "missing_forward_result"
	case parseErr != nil:
		d.Code = parseErr.Error()
	case !d.IdentityStable:
		d.Code = "account_identity_or_route_changed"
	case d.ActualChannel != route.Channel:
		d.Code = "channel_mismatch"
	case result.UpstreamResponseModelConflict:
		d.Code = "response_model_conflict"
	case d.ResponseModel == "" || d.ResponseModel != d.UpstreamModel:
		d.Code = "response_model_unconfirmed_or_mismatch"
	case d.EffectiveEffort == "":
		d.Code = "effective_effort_unconfirmed"
	case effortMismatch:
		d.Code = "response_effort_mismatch"
	default:
		d.Code = "ok"
	}
	return response, d
}

func controlledAccountIdentity(a *Account) []string {
	if a == nil {
		return nil
	}
	// Compare identity locally; never retain tokens or subject identifiers in reports.
	identity := []string{a.Platform, a.Type, a.GetCredential("chatgpt_account_id"), a.GetCredential("account_id"), a.GetCredential("user_id"), a.GetCredential("email"), a.GetCredential("base_url")}
	if a.Proxy != nil {
		identity = append(identity, a.Proxy.URL())
	}
	if a.Type == AccountTypeAPIKey {
		identity = append(identity, a.GetCredential("api_key"))
	}
	return identity
}

func (g *controlledExperimentGateway) identitySnapshot(ctx context.Context, account *Account) ([]string, error) {
	if account == nil {
		return nil, errors.New("account identity unavailable")
	}
	identity := controlledAccountIdentity(account)
	if account.ParentAccountID != nil {
		parent, err := g.accounts.GetByID(ctx, *account.ParentAccountID)
		if err != nil || parent == nil {
			return nil, errors.New("parent identity unavailable")
		}
		identity = append(identity, controlledAccountIdentity(parent)...)
	}
	return identity, nil
}

// Accept exactly one successful terminal, reject malformed/event-after-terminal
// streams, and assemble item.done records when an upstream omits terminal output.
func parseControlledResponse(wire []byte) (*ControlledTurn, string, string, string, error) {
	var terminal []byte
	terminalType, observedEffort := "", ""
	items := map[int]json.RawMessage{}
	var responseID string
	done := false
	consume := func(data []byte) error {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			if terminal == nil || done {
				return errors.New("invalid_done_marker")
			}
			done = true
			return nil
		}
		if done {
			return errors.New("event_after_done")
		}
		if !gjson.ValidBytes(data) {
			return errors.New("invalid_sse_json")
		}
		if _, err := decodeControlledJSON(data); err != nil {
			return errors.New("invalid_sse_json")
		}
		event := gjson.GetBytes(data, "type").String()
		if terminal != nil {
			return errors.New("event_after_terminal")
		}
		if event == "error" || event == "response.failed" || event == "response.incomplete" || gjson.GetBytes(data, "error").Exists() {
			return errors.New("failed_or_incomplete_terminal")
		}
		if id := gjson.GetBytes(data, "response.id").String(); id != "" {
			if responseID != "" && responseID != id {
				return errors.New("response_id_conflict")
			}
			responseID = id
		}
		if effort := gjson.GetBytes(data, "response.reasoning.effort").String(); effort != "" {
			if observedEffort != "" && observedEffort != effort {
				return errors.New("response_effort_conflict")
			}
			observedEffort = effort
		}
		if event == "response.output_item.done" {
			index := gjson.GetBytes(data, "output_index")
			item := gjson.GetBytes(data, "item")
			if !index.Exists() || index.Int() < 0 || index.Int() > 128 || !item.IsObject() {
				return errors.New("invalid_output_item")
			}
			if _, exists := items[int(index.Int())]; exists {
				return errors.New("duplicate_output_item")
			}
			items[int(index.Int())] = json.RawMessage(item.Raw)
		}
		if event == "response.completed" || event == "response.done" {
			terminal = []byte(gjson.GetBytes(data, "response").Raw)
			terminalType = event
		}
		return nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(wire))
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var data []byte
	for scanner.Scan() {
		line := bytes.TrimSuffix(scanner.Bytes(), []byte("\r"))
		if len(line) == 0 {
			if len(data) > 0 {
				if err := consume(data); err != nil {
					return nil, "", "", terminalType, err
				}
				data = nil
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, bytes.TrimSpace(line[5:])...)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, "", "", terminalType, errors.New("sse_line_limit")
	}
	if len(data) > 0 {
		if err := consume(data); err != nil {
			return nil, "", "", terminalType, err
		}
	}
	if terminal == nil {
		return nil, "", "", terminalType, errors.New("missing_terminal")
	}
	model := gjson.GetBytes(terminal, "model").String()
	if gjson.GetBytes(terminal, "status").String() != "completed" || gjson.GetBytes(terminal, "id").String() == "" {
		return nil, model, observedEffort, terminalType, errors.New("invalid_terminal")
	}
	var output []json.RawMessage
	if value := gjson.GetBytes(terminal, "output"); value.IsArray() {
		_ = json.Unmarshal([]byte(value.Raw), &output)
	}
	if len(output) == 0 && len(items) > 0 {
		for i := 0; i < len(items); i++ {
			raw, ok := items[i]
			if !ok {
				return nil, model, observedEffort, terminalType, errors.New("output_index_gap")
			}
			output = append(output, raw)
		}
	}
	if len(items) > len(output) {
		return nil, model, observedEffort, terminalType, errors.New("output_item_conflict")
	}
	usage := gjson.GetBytes(terminal, "usage")
	validTokens := func(value gjson.Result) bool {
		return value.Type == gjson.Number && value.Float() >= 0 && value.Float() == float64(value.Int())
	}
	turn := &ControlledTurn{Output: output, usagePresent: usage.IsObject() && validTokens(usage.Get("input_tokens")) && validTokens(usage.Get("output_tokens"))}
	var text strings.Builder
	calls := map[string]bool{}
	for index, raw := range output {
		if item, ok := items[index]; ok {
			a, ea := decodeControlledJSON(raw)
			b, eb := decodeControlledJSON(item)
			if ea != nil || eb != nil || !reflect.DeepEqual(a, b) {
				return nil, model, observedEffort, terminalType, errors.New("output_item_conflict")
			}
		}
		switch gjson.GetBytes(raw, "type").String() {
		case "reasoning":
		case "message":
			if gjson.GetBytes(raw, "role").String() != "assistant" {
				return nil, model, observedEffort, terminalType, errors.New("invalid_output_role")
			}
			for _, part := range gjson.GetBytes(raw, "content").Array() {
				if part.Get("type").String() == "output_text" {
					_, _ = text.WriteString(part.Get("text").String())
				} else if part.Get("type").String() == "refusal" {
					return nil, model, observedEffort, terminalType, errors.New("refusal")
				} else {
					return nil, model, observedEffort, terminalType, errors.New("unsupported_content_type")
				}
			}
		case "function_call":
			id := gjson.GetBytes(raw, "call_id").String()
			if id == "" || calls[id] {
				return nil, model, observedEffort, terminalType, errors.New("invalid_tool_call_id")
			}
			calls[id] = true
		default:
			return nil, model, observedEffort, terminalType, errors.New("unsupported_output_type")
		}
	}
	turn.Text = text.String()
	return turn, model, observedEffort, terminalType, nil
}
