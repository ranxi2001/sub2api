package service

import (
	"context"
	"math"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Collect only group-test requests. Each sample owns its collector, and each
// upstream stream owns a snapshot so cumulative usage is never added twice.
type pelicanTestUsageKey struct{}

type pelicanTestUsageCollector struct {
	model    string
	requests []*pelicanTestUsage
}

type pelicanTestUsage struct {
	ciallo      *CialloEvidence
	protocol    string
	model       string
	serviceTier string
	tokens      UsageTokens
	input       int
	output      int
	thoughts    int
	seen        bool
	inputSeen   bool
	outputSeen  bool
	complete    bool
}

func startPelicanTestStream(c *gin.Context, protocol string) *pelicanTestUsage {
	if c.Request == nil {
		return nil
	}
	return startPelicanTestUsage(c.Request.Context(), protocol)
}

func recordPelicanTestSSE(ctx context.Context, protocol, model string, body []byte) {
	u := startPelicanTestUsage(ctx, protocol)
	if u == nil {
		return
	}
	u.model = model
	for _, line := range strings.Split(string(body), "\n") {
		if data, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok {
			u.read(strings.TrimSpace(data))
		}
	}
}

func pelicanUsageFromContext(ctx context.Context) *pelicanTestUsageCollector {
	u, _ := ctx.Value(pelicanTestUsageKey{}).(*pelicanTestUsageCollector)
	return u
}

func startPelicanTestUsage(ctx context.Context, protocol string) *pelicanTestUsage {
	collector := pelicanUsageFromContext(ctx)
	evidence, _ := ctx.Value(cialloEvidenceKey{}).(*CialloEvidence)
	if evidence != nil {
		evidence.Protocol = protocol
		evidence.complete = false
		evidence.interrupted = false
		evidence.endTurn = false
	}
	if collector == nil {
		if evidence == nil {
			return nil
		}
		return &pelicanTestUsage{protocol: protocol, ciallo: evidence}
	}
	u := &pelicanTestUsage{protocol: protocol, model: collector.model, ciallo: evidence}
	collector.requests = append(collector.requests, u)
	return u
}

func (u *pelicanTestUsage) read(raw string) {
	if u == nil {
		return
	}
	data := gjson.Parse(raw)
	eventType := data.Get("type").String()
	if u.ciallo != nil {
		u.ciallo.observe(data, eventType)
	}
	if eventType == "message_stop" || eventType == "response.completed" || eventType == "response.done" || eventType == "response.failed" || eventType == "response.incomplete" {
		u.complete = true
	}
	if response := data.Get("response"); response.IsObject() {
		data = response
	} else if message := data.Get("message"); message.IsObject() {
		data = message
	}
	if model := strings.TrimSpace(data.Get("model").String()); model != "" {
		u.model = model
	}
	if tier := data.Get("service_tier").String(); tier != "" {
		u.serviceTier = tier
	}
	usage := data.Get("usage")
	if u.protocol == "gemini" {
		usage = data.Get("usageMetadata")
		for _, candidate := range data.Get("candidates").Array() {
			if candidate.Get("finishReason").String() != "" {
				u.complete = true
			}
		}
	}
	for _, choice := range data.Get("choices").Array() {
		if choice.Get("finish_reason").String() != "" {
			u.complete = true
		}
	}
	if !usage.IsObject() {
		return
	}
	set := func(field string, value *int) {
		if n := usage.Get(field); n.Type == gjson.Number && n.Float() >= 0 {
			*value = int(n.Int())
			u.seen = true
			if value == &u.input {
				u.inputSeen = true
			}
			if value == &u.output {
				u.outputSeen = true
			}
		}
	}
	switch u.protocol {
	case "anthropic":
		set("input_tokens", &u.input)
		set("output_tokens", &u.output)
		set("cache_read_input_tokens", &u.tokens.CacheReadTokens)
		set("cache_creation_input_tokens", &u.tokens.CacheCreationTokens)
		set("cache_creation.ephemeral_5m_input_tokens", &u.tokens.CacheCreation5mTokens)
		set("cache_creation.ephemeral_1h_input_tokens", &u.tokens.CacheCreation1hTokens)
	case "gemini":
		set("promptTokenCount", &u.input)
		set("candidatesTokenCount", &u.output)
		set("thoughtsTokenCount", &u.thoughts)
		set("cachedContentTokenCount", &u.tokens.CacheReadTokens)
	case "chat":
		set("prompt_tokens", &u.input)
		set("completion_tokens", &u.output)
		set("prompt_tokens_details.cached_tokens", &u.tokens.CacheReadTokens)
		set("prompt_cache_hit_tokens", &u.tokens.CacheReadTokens)
	default:
		set("input_tokens", &u.input)
		set("output_tokens", &u.output)
		set("input_tokens_details.cached_tokens", &u.tokens.CacheReadTokens)
		set("cache_read_input_tokens", &u.tokens.CacheReadTokens)
		set("cache_creation_input_tokens", &u.tokens.CacheCreationTokens)
	}
	u.tokens.InputTokens = u.input
	if u.protocol != "anthropic" {
		// OpenAI/Chat/Gemini input totals already include cache hits.
		u.tokens.InputTokens = max(0, u.input-u.tokens.CacheReadTokens-u.tokens.CacheCreationTokens)
	}
	u.tokens.OutputTokens = u.output + u.thoughts
}

// Return a cost snapshot, not a debit. Use the account's upstream cost multiplier,
// never the customer/group selling multiplier. Missing usage/pricing remains
// unknown; a partially metered attempt must not discard other known costs.
func (c *pelicanTestUsageCollector) cost(billing *BillingService, account *Account) (*float64, bool) {
	var total float64
	priced, incomplete := false, len(c.requests) == 0
	for _, u := range c.requests {
		if !u.seen || billing == nil {
			incomplete = true
			continue
		}
		cost, err := billing.CalculateCostWithServiceTier(u.model, u.tokens, 1, u.serviceTier)
		if err != nil || cost == nil {
			incomplete = true
			continue
		}
		amount := cost.TotalCost * account.CostMultiplier()
		if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
			incomplete = true
			continue
		}
		total += amount
		priced = true
		incomplete = incomplete || !u.complete || !u.inputSeen || !u.outputSeen
	}
	if !priced {
		return nil, true
	}
	return &total, incomplete
}
