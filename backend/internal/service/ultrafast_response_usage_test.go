//go:build unit

package service

// Offline fixtures exercise RecordUsage and durable delivery code. The
// repository below models deduplication; it does not prove SQL transactions.
import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/tidwall/gjson"
)

type ultrafastUsageRepo struct {
	UsageBillingRepository
	events                         map[UsageBillingEventKey]UsageBillingEvent
	applied                        map[UsageBillingEventKey]bool
	pending                        map[UsageBillingEventKey]bool
	commands                       []UsageBillingCommand
	applications, acknowledgements int
}

func (r *ultrafastUsageRepo) StageUsageBilling(_ context.Context, cmd *UsageBillingCommand, usage *UsageLog) error {
	r.commands = append(r.commands, *cmd)
	k := UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID}
	if prev, ok := r.events[k]; ok {
		if prev.Command.RequestFingerprint != cmd.RequestFingerprint {
			return ErrUsageBillingRequestConflict
		}
		return nil
	}
	r.events[k] = UsageBillingEvent{Version: 1, Command: *cmd, Usage: *usage}
	return nil
}
func (r *ultrafastUsageRepo) ApplyStagedUsageBilling(_ context.Context, k UsageBillingEventKey) (*UsageBillingRecovery, error) {
	e, ok := r.events[k]
	if !ok {
		return nil, errors.New("fixture event missing")
	}
	first := !r.applied[k]
	if first {
		r.applications++
		r.applied[k] = true
		r.pending[k] = true
	}
	return &UsageBillingRecovery{Event: e, Result: UsageBillingApplyResult{Applied: first}, NeedsCache: r.pending[k]}, nil
}
func (r *ultrafastUsageRepo) PendingUsageBilling(_ context.Context, _ int) ([]UsageBillingEventKey, error) {
	var keys []UsageBillingEventKey
	for k, yes := range r.pending {
		if yes {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
func (r *ultrafastUsageRepo) AcknowledgeUsageBillingCache(_ context.Context, k UsageBillingEventKey) error {
	r.acknowledgements++
	r.pending[k] = false
	return nil
}

type ultrafastUsageCache struct {
	BillingCache
	balance, subscription, key, platform int
	failBalanceOnce                      bool
}

func (c *ultrafastUsageCache) InvalidateUserBalance(context.Context, int64) error {
	c.balance++
	if c.failBalanceOnce {
		c.failBalanceOnce = false
		return errors.New("fixture injected cache failure")
	}
	return nil
}
func (c *ultrafastUsageCache) InvalidateSubscriptionCache(context.Context, int64, int64) error {
	c.subscription++
	return nil
}
func (c *ultrafastUsageCache) InvalidateAPIKeyRateLimit(context.Context, int64) error {
	c.key++
	return nil
}
func (c *ultrafastUsageCache) DeleteUserPlatformQuotaCache(context.Context, int64, string) error {
	c.platform++
	return nil
}

type ultrafastUsageKeyUpdater struct{}

func (*ultrafastUsageKeyUpdater) UpdateQuotaUsed(context.Context, int64, float64) error {
	panic("durable path must not replay legacy quota delta")
}
func (*ultrafastUsageKeyUpdater) UpdateRateLimitUsage(context.Context, int64, float64) error {
	panic("durable path must not replay legacy window delta")
}

func ultrafastUsageFloat(v float64) *float64 { return &v }
func ultrafastUsageNear(t *testing.T, label string, want, got float64) {
	t.Helper()
	if math.Abs(want-got) > 1e-10 {
		t.Errorf("%s: want %.12f got %.12f", label, want, got)
	}
}

func ultrafastUsageGroup() *Group {
	return &Group{ID: 1004, Platform: PlatformOpenAI, RateMultiplier: .25, LongContextPricingEnabled: true}
}

func ultrafastUsageHarness(t *testing.T, group *Group, enabled bool) (*OpenAIGatewayService, *OpenAIRecordUsageInput, *ultrafastUsageRepo, *ultrafastUsageCache) {
	t.Helper()
	data, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	if err != nil {
		t.Fatal(err)
	}
	catalog := &PricingService{}
	catalog.pricingData, err = catalog.parsePricingData(data)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	billing := NewBillingService(cfg, catalog)
	repo := &ultrafastUsageRepo{events: make(map[UsageBillingEventKey]UsageBillingEvent), applied: make(map[UsageBillingEventKey]bool), pending: make(map[UsageBillingEventKey]bool)}
	cache := &ultrafastUsageCache{}
	service := &OpenAIGatewayService{cfg: cfg, billingService: billing, resolver: NewModelPricingResolver(nil, billing),
		usageBillingRepo: repo, billingCacheService: &BillingCacheService{cache: cache, cfg: cfg}, deferredService: &DeferredService{}}
	input := &OpenAIRecordUsageInput{
		APIKey: &APIKey{ID: 1002, GroupID: &group.ID, Group: group, Quota: 100, RateLimit5h: 100}, User: &User{ID: 1003, Balance: 100},
		Account: &Account{ID: 1001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, RateMultiplier: ultrafastUsageFloat(.7),
			Extra: map[string]any{OpenAIResponseTierBillingEnabledKey: enabled, "quota_limit": float64(1000)}},
		APIKeyService: &ultrafastUsageKeyUpdater{}, PricingAt: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
	}
	return service, input, repo, cache
}

func ultrafastUsageResult(t *testing.T, model, requested, observed string, total, read, write, output int) *OpenAIForwardResult {
	t.Helper()
	// A model-free terminal event must still supply the authoritative tier.
	body := []byte(fmt.Sprintf(`{"response":{"service_tier":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"input_tokens_details":{"cached_tokens":%d,"cache_write_tokens":%d}}}}`, observed, total, output, read, write))
	observer := &upstreamResponseModelObserver{}
	observer.ObserveOpenAI(body, "response.completed")
	usage, ok := openAIUsageFromGJSON(gjson.GetBytes(body, "response.usage"))
	if !ok {
		t.Fatal("usage parser did not accept offline fixture")
	}
	r := &OpenAIForwardResult{RequestID: "fixture-ultrafast-request", Model: model, Usage: usage, UpstreamResponseServiceTier: observer.ServiceTier()}
	if requested != "" {
		r.ServiceTier = &requested
	}
	return r
}

func ultrafastUsageEvent(t *testing.T, repo *ultrafastUsageRepo) UsageBillingEvent {
	t.Helper()
	if len(repo.events) != 1 {
		t.Fatalf("want one event, got %d", len(repo.events))
	}
	for _, e := range repo.events {
		return e
	}
	panic("unreachable")
}

func TestUltrafastUsageRecordUsageCachedTokenMatrix(t *testing.T) {
	for _, model := range []struct {
		name                       string
		input, output, read, write float64
	}{
		{"gpt-5.6-sol", 5e-6, 30e-6, .5e-6, 6.25e-6},
		{"gpt-5.6-terra", 2e-6, 12e-6, .2e-6, 2.5e-6},
		{"gpt-6-astra", 10e-6, 50e-6, 1e-6, 12.5e-6},
		{"gpt-6-sol", 2e-6, 10e-6, .2e-6, 2.5e-6},
		{"gpt-6.1-sol", 2e-6, 10e-6, .1e-6, 2.5e-6},
		{"gpt-6.1-terra", 1e-6, 5e-6, .05e-6, 1.25e-6},
	} {
		for _, total := range []int{10000, 272000, 272001, 500001} {
			for _, tier := range []struct {
				name, requested, observed, billed string
				factor                            float64
			}{
				{"response", "", " UltraFast ", "ultrafast", 6},
				{"fallback", "ultrafast", "", "ultrafast", 6},
				{"downgrade", "ultrafast", "default", "default", 1},
			} {
				t.Run(fmt.Sprintf("%s/%d/%s", model.name, total, tier.name), func(t *testing.T) {
					svc, input, repo, cache := ultrafastUsageHarness(t, ultrafastUsageGroup(), true)
					input.Result = ultrafastUsageResult(t, model.name, tier.requested, tier.observed, total, total-2000, 1000, 500)
					if err := svc.RecordUsage(context.Background(), input); err != nil {
						t.Fatal(err)
					}
					e := ultrafastUsageEvent(t, repo)
					im, om := 1.0, 1.0
					if total > 272000 {
						im, om = 2, 1.5
					}
					wantIn := 1000 * model.input * im * tier.factor
					wantOut := 500 * model.output * om * tier.factor
					wantRead := float64(total-2000) * model.read * im * tier.factor
					wantWrite := 1000 * model.write * im * tier.factor
					wantTotal := wantIn + wantOut + wantRead + wantWrite
					wantActual := QuantizeUsageBillingAmount(wantTotal * .25)
					ultrafastUsageNear(t, "ordinary input", wantIn, e.Usage.InputCost)
					ultrafastUsageNear(t, "output", wantOut, e.Usage.OutputCost)
					ultrafastUsageNear(t, "cache read", wantRead, e.Usage.CacheReadCost)
					ultrafastUsageNear(t, "cache write", wantWrite, e.Usage.CacheCreationCost)
					ultrafastUsageNear(t, "total", wantTotal, e.Usage.TotalCost)
					for name, amount := range map[string]float64{"log": e.Usage.ActualCost, "balance": e.Command.BalanceCost, "key quota": e.Command.APIKeyQuotaCost, "key window": e.Command.APIKeyRateLimitCost, "platform": e.Command.PlatformQuotaCost} {
						ultrafastUsageNear(t, name, wantActual, amount)
					}
					ultrafastUsageNear(t, "independent account cost", QuantizeUsageBillingAmount(wantTotal*.7), e.Command.AccountQuotaCost)
					if e.Command.InputTokens != 1000 || e.Command.CacheReadTokens != total-2000 || e.Command.CacheCreationTokens != 1000 {
						t.Errorf("overlapping token buckets: %+v", e.Command)
					}
					if e.Command.ServiceTier != tier.billed {
						t.Errorf("billed tier: %s", e.Command.ServiceTier)
					}
					if repo.applications != 1 || repo.acknowledgements != 1 || cache.balance != 1 || cache.key != 1 || cache.platform != 1 {
						t.Errorf("unexpected durable delivery/cache counts: repo=%+v cache=%+v", repo, cache)
					}
					if model.name == "gpt-6.1-sol" && total == 272001 && tier.name == "response" {
						t.Logf("sample raw_input=%d ordinary=1000 read=%d write=1000 input=$%.8f output=$%.8f read=$%.8f write=$%.8f total=$%.8f balance=$%.8f", total, total-2000, e.Usage.InputCost, e.Usage.OutputCost, e.Usage.CacheReadCost, e.Usage.CacheCreationCost, e.Usage.TotalCost, e.Command.BalanceCost)
					}
				})
			}
		}
	}
}

func TestUltrafastUsageDurableRetryAndCacheRecovery(t *testing.T) {
	for _, failOnce := range []bool{false, true} {
		t.Run(fmt.Sprintf("cache_failure=%t", failOnce), func(t *testing.T) {
			svc, input, repo, cache := ultrafastUsageHarness(t, ultrafastUsageGroup(), true)
			cache.failBalanceOnce = failOnce
			input.Result = ultrafastUsageResult(t, "gpt-6.1-sol", "priority", "ultrafast", 272001, 270001, 1000, 500)
			err := svc.RecordUsage(context.Background(), input)
			if (err != nil) != failOnce {
				t.Fatalf("unexpected first result: %v", err)
			}
			if failOnce {
				if repo.acknowledgements != 0 {
					t.Fatal("failed cache invalidation was acknowledged")
				}
				if err := recoverUsageBillingBatch(context.Background(), repo, svc.billingCacheService, nil); err != nil {
					t.Fatal(err)
				}
			}
			// Reconstruct the same request; do not reuse the mutated result.
			input.Result = ultrafastUsageResult(t, "gpt-6.1-sol", "priority", "ultrafast", 272001, 270001, 1000, 500)
			if err := svc.RecordUsage(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			if repo.applications != 1 || repo.acknowledgements != 1 {
				t.Errorf("duplicate monetary execution: %d, ack %d", repo.applications, repo.acknowledgements)
			}
			if len(repo.commands) != 2 || repo.commands[0].RequestFingerprint != repo.commands[1].RequestFingerprint {
				t.Fatal("unstable billing fingerprint")
			}
			wantInvalidations := 1
			if failOnce {
				wantInvalidations = 2
			}
			if cache.balance != wantInvalidations || cache.key != wantInvalidations || cache.platform != wantInvalidations {
				t.Errorf("cache retry counts: %+v", cache)
			}
		})
	}
}

func TestUltrafastUsageAccountGroupMultiplierAndZeroRate(t *testing.T) {
	for _, multiplier := range []float64{0, .8, 2} {
		t.Run(fmt.Sprint(multiplier), func(t *testing.T) {
			svc, input, repo, _ := ultrafastUsageHarness(t, ultrafastUsageGroup(), true)
			input.Account.GroupRateMultiplier = ultrafastUsageFloat(multiplier)
			input.Result = ultrafastUsageResult(t, "gpt-6-sol", "", "ultrafast", 10000, 8000, 1000, 500)
			if err := svc.RecordUsage(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			e := ultrafastUsageEvent(t, repo)
			standard := 1000*2e-6 + 500*10e-6 + 8000*.2e-6 + 1000*2.5e-6
			ultrafastUsageNear(t, "charge with independent account group multiplier", QuantizeUsageBillingAmount(standard*6*.25*multiplier), e.Command.BalanceCost)
		})
	}
}

func TestUltrafastUsageCacheTTLAndNoPricingCacheContamination(t *testing.T) {
	pricing := &ModelPricing{InputPricePerToken: 2e-6, OutputPricePerToken: 10e-6, CacheReadPricePerToken: .2e-6,
		CacheCreationPricePerToken: 2.5e-6, CacheCreation5mPrice: 2.5e-6, CacheCreation1hPrice: 4e-6, SupportsCacheBreakdown: true,
		LongContextInputThreshold: 272000, LongContextInputMultiplier: 2, LongContextOutputMultiplier: 1.5}
	svc := &BillingService{}
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 270001, CacheCreationTokens: 1000, CacheCreation5mTokens: 400, CacheCreation1hTokens: 600}
	for _, tier := range []struct {
		name   string
		factor float64
	}{{"default", 1}, {"ultrafast", 6}, {"default", 1}, {"ultrafast", 6}} {
		cost := svc.computeTokenBreakdown(pricing, tokens, .25, tier.name, true)
		ultrafastUsageNear(t, "mixed TTL", (400*2.5e-6+600*4e-6)*2*tier.factor, cost.CacheCreationCost)
		ultrafastUsageNear(t, "read", 270001*.2e-6*2*tier.factor, cost.CacheReadCost)
	}
	ultrafastUsageNear(t, "original cached price unchanged", 2e-6, pricing.InputPricePerToken)
	ultrafastUsageNear(t, "original cached read unchanged", .2e-6, pricing.CacheReadPricePerToken)
}

func TestUltrafastUsageExplicitZeroCacheWrite(t *testing.T) {
	group := ultrafastUsageGroup()
	group.ModelPricing = []ChannelModelPricing{{Models: []string{"gpt-6.1-sol"}, BillingMode: BillingModeToken,
		InputPrice: ultrafastUsageFloat(2e-6), OutputPrice: ultrafastUsageFloat(10e-6), CacheReadPrice: ultrafastUsageFloat(.1e-6), CacheWritePrice: ultrafastUsageFloat(0)}}
	svc, input, repo, _ := ultrafastUsageHarness(t, group, true)
	input.Result = ultrafastUsageResult(t, "gpt-6.1-sol", "", "ultrafast", 272001, 270001, 1000, 500)
	if err := svc.RecordUsage(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	e := ultrafastUsageEvent(t, repo)
	ultrafastUsageNear(t, "explicit free cache creation stays zero", 0, e.Usage.CacheCreationCost)
}

// The account switch controls response-based settlement. A client explicitly
// requesting UltraFast still pays 6x when that switch is off or absent.
func TestUltrafastUsageExplicitRequestWithoutResponseBilling(t *testing.T) {
	for _, flag := range []any{nil, false, true} {
		for _, observed := range []string{"", "ultrafast"} {
			t.Run(fmt.Sprintf("enabled=%v/observed=%s", flag, observed), func(t *testing.T) {
				svc, input, repo, _ := ultrafastUsageHarness(t, ultrafastUsageGroup(), false)
				if flag == nil {
					delete(input.Account.Extra, OpenAIResponseTierBillingEnabledKey)
				} else {
					input.Account.Extra[OpenAIResponseTierBillingEnabledKey] = flag
				}
				input.Result = ultrafastUsageResult(t, "gpt-6-sol", "ultrafast", observed, 10000, 8000, 1000, 500)
				if err := svc.RecordUsage(context.Background(), input); err != nil {
					t.Fatal(err)
				}
				e := ultrafastUsageEvent(t, repo)
				ultrafastUsageNear(t, "explicit UltraFast is always 6x", .01665, e.Command.BalanceCost)
			})
		}
	}
}
