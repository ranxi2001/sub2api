package service

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// Idle probes keep quiet groups from turning grey on the V3 page. For the
// groups an admin checks, a slot that is about to end with fewer real successes
// than the page minimum gets one short request: V1's arithmetic challenge,
// sent through the same group scheduler a user request reaches (the path V2's
// candy checks use). Probes are not billed to users, are not written to usage
// logs and never enter the passive facts; the page reads them separately and
// only for slots whose real traffic is too thin to judge.

const (
	ChannelMonitorV3ProbeSuccess = "success"
	ChannelMonitorV3ProbeFailure = "failure"

	// ChannelMonitorV3ProbeQuestionKind marks the arithmetic probe on the
	// Pelican account-test path, which then only requires a non-empty answer.
	ChannelMonitorV3ProbeQuestionKind = "monitor_probe"
	// ChannelMonitorV3ProbeWrongAnswer is the error category of a wrong answer.
	ChannelMonitorV3ProbeWrongAnswer = "wrong_answer"

	// channelMonitorV3ProbeLead is how long before a slot ends it is probed.
	channelMonitorV3ProbeLead    = 30 * time.Second
	channelMonitorV3ProbeTimeout = 90 * time.Second
	channelMonitorV3ProbeWorkers = 4
	channelMonitorV3ProbeMessage = 300

	channelMonitorV3WrongAnswerPrefix = "answer_mismatch:"
)

// channelMonitorV3ProbeIgnoredCategories are answers to a probe the group
// cannot serve, a model or request shape it does not accept. They point at the
// probe settings rather than the channel, so they leave the slot unjudged. The
// page's user-error filter does not apply to probes: they have no user.
var channelMonitorV3ProbeIgnoredCategories = []string{"invalid_request", "model_unsupported", "not_found"}

func channelMonitorV3ProbeIgnored(category string) bool {
	return slices.Contains(channelMonitorV3ProbeIgnoredCategories, category)
}

type ChannelMonitorV3IdleProbe struct {
	Enabled bool                             `json:"enabled"`
	Groups  []ChannelMonitorV3IdleProbeGroup `json:"groups"`
}

type ChannelMonitorV3IdleProbeGroup struct {
	GroupID int64 `json:"group_id"`
	// Model is asked for components that cover every model of the group;
	// components narrowed to one model probe that model.
	Model string `json:"model"`
	// ReasoningEffort is passed to the account test; empty keeps the model default.
	ReasoningEffort string `json:"reasoning_effort"`
}

// probedGroups returns the groups among ids whose probes the page uses.
func (p ChannelMonitorV3IdleProbe) probedGroups(ids []int64) []int64 {
	if !p.Enabled {
		return nil
	}
	out := []int64{}
	for _, id := range ids {
		if slices.ContainsFunc(p.Groups, func(g ChannelMonitorV3IdleProbeGroup) bool { return g.GroupID == id }) {
			out = append(out, id)
		}
	}
	return out
}

// ChannelMonitorV3ProbeFact counts finished probes of one group, model and
// outcome; Category is empty for successful probes. Slot is zero for totals.
type ChannelMonitorV3ProbeFact struct {
	GroupID  int64
	Model    string
	Slot     time.Time
	Category string
	Count    int64
	// LatencyP50Ms is the median latency of the successful probes counted here.
	LatencyP50Ms int64
}

// ChannelMonitorV3ProbeResult is what a finished probe writes back.
type ChannelMonitorV3ProbeResult struct {
	ID           int64
	Outcome      string
	LatencyMs    int64
	Category     string
	ErrorMessage string
	AccountID    int64
	CostUSD      *float64
}

// ChannelMonitorV3IdleProbeStat summarises one group's recent probes for the admin.
type ChannelMonitorV3IdleProbeStat struct {
	GroupID  int64   `json:"group_id"`
	Probes   int64   `json:"probes"`
	Failures int64   `json:"failures"`
	CostUSD  float64 `json:"cost_usd"`
	// Last* describe the newest finished probe, which may be older than a day.
	LastAt        *time.Time `json:"last_at"`
	LastModel     string     `json:"last_model,omitempty"`
	LastOutcome   string     `json:"last_outcome,omitempty"`
	LastLatencyMs *int64     `json:"last_latency_ms,omitempty"`
	LastCategory  string     `json:"last_category,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
}

// UpdateIdleProbe saves which groups are probed and how. A checked group with
// components covering every model needs a model to ask.
func (s *ChannelMonitorV3Service) UpdateIdleProbe(ctx context.Context, input ChannelMonitorV3IdleProbe) (*ChannelMonitorV3IdleProbe, error) {
	if err := normalizeChannelMonitorV3IdleProbe(&input); err != nil {
		return nil, err
	}
	components, err := s.repo.ListComponents(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range input.Groups {
		if s.groups != nil {
			if group, err := s.groups.GetByID(ctx, g.GroupID); err != nil || group == nil {
				return nil, channelMonitorV3Invalid("group %d does not exist", g.GroupID)
			}
		}
		if g.Model == "" && slices.ContainsFunc(components, func(c ChannelMonitorV3Component) bool { return c.GroupID == g.GroupID && c.Model == "" }) {
			return nil, channelMonitorV3Invalid("group %d has components covering every model; set the model to probe", g.GroupID)
		}
	}
	if err := s.repo.UpdateIdleProbe(ctx, input); err != nil {
		return nil, err
	}
	return &input, nil
}

func normalizeChannelMonitorV3IdleProbe(probe *ChannelMonitorV3IdleProbe) error {
	if len(probe.Groups) > channelMonitorV3MaxComponents {
		return channelMonitorV3Invalid("at most %d probed groups", channelMonitorV3MaxComponents)
	}
	groups := make([]ChannelMonitorV3IdleProbeGroup, 0, len(probe.Groups))
	for _, g := range probe.Groups {
		if g.GroupID <= 0 || slices.ContainsFunc(groups, func(seen ChannelMonitorV3IdleProbeGroup) bool { return seen.GroupID == g.GroupID }) {
			return channelMonitorV3Invalid("probed groups must be distinct groups")
		}
		g.Model = strings.TrimSpace(g.Model)
		if utf8.RuneCountInString(g.Model) > 100 {
			return channelMonitorV3Invalid("probe model must be at most 100 characters")
		}
		if effort := strings.TrimSpace(g.ReasoningEffort); effort != "" {
			if g.ReasoningEffort = normalizePelicanReasoningEffort(effort); g.ReasoningEffort == "" {
				return channelMonitorV3Invalid("unknown reasoning effort %q", effort)
			}
		} else {
			g.ReasoningEffort = ""
		}
		groups = append(groups, g)
	}
	probe.Groups = groups
	return nil
}

// attachProbes adds the finished probes of the probed groups to facts.
func (s *ChannelMonitorV3Service) attachProbes(ctx context.Context, facts *ChannelMonitorV3Facts, groups []int64, from, to time.Time, interval time.Duration) error {
	if facts == nil || len(groups) == 0 {
		return nil
	}
	probes, err := s.repo.ProbeFacts(ctx, groups, from, to, interval)
	if err != nil {
		return err
	}
	facts.Probes = probes
	return nil
}

// channelMonitorV3ProbeRetention keeps probes for the availability range and
// at least the seven days bars and incidents page back, plus a day.
func channelMonitorV3ProbeRetention(availabilityRange string) time.Duration {
	keep := channelMonitorV3Ranges[availabilityRange]
	if keep < ChannelMonitorV3History {
		keep = ChannelMonitorV3History
	}
	return keep + 24*time.Hour
}

// ChannelMonitorV3Prober runs the idle probes on the instance that runs
// background jobs. It wakes 30 seconds into every minute and acts only in the
// last minute of a slot; several instances share the work through the claim.
type ChannelMonitorV3Prober struct {
	repo     ChannelMonitorV3Repository
	groups   *PelicanGroupTestService
	settings channelMonitorRuntimeReader
	now      func() time.Time

	tick     sync.Mutex
	prunedAt time.Time

	ctx       context.Context
	cancel    context.CancelFunc
	startOnce sync.Once
	stopOnce  sync.Once
}

func NewChannelMonitorV3Prober(repo ChannelMonitorV3Repository, groups *PelicanGroupTestService, settings channelMonitorRuntimeReader) *ChannelMonitorV3Prober {
	ctx, cancel := context.WithCancel(context.Background())
	return &ChannelMonitorV3Prober{repo: repo, groups: groups, settings: settings, now: time.Now, ctx: ctx, cancel: cancel}
}

func (p *ChannelMonitorV3Prober) Start() {
	if p == nil || p.repo == nil || p.groups == nil {
		return
	}
	p.startOnce.Do(func() { go p.loop() })
}

// Stop cancels the loop and any probe in flight; a cancelled probe is saved as a failure.
func (p *ChannelMonitorV3Prober) Stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(p.cancel)
}

func (p *ChannelMonitorV3Prober) loop() {
	for {
		now := p.now()
		next := now.Truncate(time.Minute).Add(time.Minute - channelMonitorV3ProbeLead)
		if !next.After(now) {
			next = next.Add(time.Minute)
		}
		timer := time.NewTimer(next.Sub(now))
		select {
		case <-p.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		p.RunDue(p.ctx, next)
	}
}

// RunDue probes the idle targets of the slot that ends within a minute of now.
// It returns once those probes finished; ticks that arrive meanwhile are skipped.
func (p *ChannelMonitorV3Prober) RunDue(ctx context.Context, now time.Time) {
	if p == nil || !p.tick.TryLock() {
		return
	}
	defer p.tick.Unlock()
	now = now.UTC()
	cfg, err := p.repo.GetConfig(ctx)
	if err != nil {
		logger.LegacyPrintf("service.channel_monitor_v3", "idle probe config failed: %v", err)
		return
	}
	p.prune(ctx, cfg.AvailabilityRange, now)
	if !cfg.IdleProbe.Enabled || len(cfg.IdleProbe.Groups) == 0 || p.settings == nil || !p.settings.GetChannelMonitorRuntime(ctx).V3Active() {
		return
	}
	interval := time.Duration(cfg.IntervalMinutes) * time.Minute
	slot := now.Truncate(interval)
	if slot.Add(interval).Sub(now) > time.Minute {
		return
	}
	components, err := p.repo.ListComponents(ctx)
	if err != nil {
		logger.LegacyPrintf("service.channel_monitor_v3", "idle probe components failed: %v", err)
		return
	}
	targets := channelMonitorV3ProbeTargets(cfg.IdleProbe, components)
	if len(targets) == 0 {
		return
	}
	groupIDs := []int64{}
	for _, t := range targets {
		if !slices.Contains(groupIDs, t.GroupID) {
			groupIDs = append(groupIDs, t.GroupID)
		}
	}
	counts, err := p.repo.SuccessCounts(ctx, groupIDs, slot, now)
	if err != nil {
		logger.LegacyPrintf("service.channel_monitor_v3", "idle probe traffic count failed: %v", err)
		return
	}
	due := channelMonitorV3DueProbes(targets, counts, cfg.MinRequests)
	jobs := make(chan channelMonitorV3ProbeTarget)
	var wg sync.WaitGroup
	for i := 0; i < min(channelMonitorV3ProbeWorkers, len(due)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for target := range jobs {
				p.runOne(ctx, target, slot, cfg.IntervalMinutes)
			}
		}()
	}
	for _, target := range due {
		select {
		case jobs <- target:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
}

func (p *ChannelMonitorV3Prober) prune(ctx context.Context, availabilityRange string, now time.Time) {
	if !p.prunedAt.IsZero() && now.Sub(p.prunedAt) < time.Hour {
		return
	}
	p.prunedAt = now
	if err := p.repo.PruneProbes(ctx, now.Add(-channelMonitorV3ProbeRetention(availabilityRange))); err != nil {
		logger.LegacyPrintf("service.channel_monitor_v3", "idle probe retention failed: %v", err)
	}
}

func (p *ChannelMonitorV3Prober) runOne(ctx context.Context, target channelMonitorV3ProbeTarget, slot time.Time, slotMinutes int) {
	if ctx.Err() != nil {
		return
	}
	group, err := p.groups.groups.GetByID(ctx, target.GroupID)
	if err != nil || group == nil || group.Status != StatusActive {
		return
	}
	id, err := p.repo.ClaimProbe(ctx, target.GroupID, target.Model, slot, slotMinutes)
	if err != nil {
		logger.LegacyPrintf("service.channel_monitor_v3", "idle probe claim group=%d failed: %v", target.GroupID, err)
		return
	}
	if id == 0 {
		return
	}
	started := p.now()
	result := ChannelMonitorV3ProbeResult{ID: id, Outcome: ChannelMonitorV3ProbeFailure, ErrorMessage: "probe_failed: interrupted"}
	abandoned := false
	defer func() {
		if recover() != nil {
			result.Outcome, result.ErrorMessage, abandoned = ChannelMonitorV3ProbeFailure, "probe_failed: panic", false
		}
		if abandoned {
			// A probe cut short by shutdown says nothing about the channel; its
			// claim stays unfinished, which the page ignores.
			return
		}
		result.LatencyMs = p.now().Sub(started).Milliseconds()
		result.ErrorMessage = channelMonitorV3Truncate(result.ErrorMessage, channelMonitorV3ProbeMessage)
		if result.Outcome != ChannelMonitorV3ProbeSuccess {
			result.Category = channelMonitorV3ProbeCategory(result.ErrorMessage)
		}
		saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.repo.FinishProbe(saveCtx, result); err != nil {
			logger.LegacyPrintf("service.channel_monitor_v3", "idle probe save group=%d failed: %v", target.GroupID, err)
		}
	}()
	challenge := generateChallenge()
	runCtx, cancel := context.WithTimeout(ctx, channelMonitorV3ProbeTimeout)
	defer cancel()
	plan := &PelicanGroupTestPlan{GroupID: target.GroupID, ModelID: target.Model, PelicanConfig: &PelicanTestConfig{
		QuestionKind: ChannelMonitorV3ProbeQuestionKind, Prompt: challenge.Prompt, ReasoningEffort: target.ReasoningEffort,
		ParallelCount: 1, ModelID: target.Model,
	}}
	sample := p.groups.runSample(runCtx, plan, group)
	if ctx.Err() != nil {
		abandoned = true
		return
	}
	if sample != nil {
		result.AccountID, result.CostUSD = sample.AccountID, sample.CostUSD
	}
	result.Outcome, result.ErrorMessage = channelMonitorV3ProbeVerdict(runCtx.Err(), sample, challenge.Expected)
}

// channelMonitorV3ProbeVerdict judges a probe the way V1 judges a check: the
// answer must contain the expected number; anything else is a failure.
func channelMonitorV3ProbeVerdict(deadline error, sample *PelicanGroupTestResult, expected string) (string, string) {
	switch {
	case deadline != nil:
		return ChannelMonitorV3ProbeFailure, fmt.Sprintf("timeout: no answer within %d seconds", int(channelMonitorV3ProbeTimeout.Seconds()))
	case sample == nil:
		return ChannelMonitorV3ProbeFailure, "probe_failed: no result"
	case sample.Status != "success":
		if strings.TrimSpace(sample.ErrorMessage) == "" {
			return ChannelMonitorV3ProbeFailure, "probe_failed: no answer"
		}
		return ChannelMonitorV3ProbeFailure, sample.ErrorMessage
	case !validateChallenge(sample.ResponseText, expected):
		return ChannelMonitorV3ProbeFailure, fmt.Sprintf("%s expected %s, got %q", channelMonitorV3WrongAnswerPrefix, expected, channelMonitorV3Truncate(strings.TrimSpace(sample.ResponseText), 80))
	default:
		return ChannelMonitorV3ProbeSuccess, ""
	}
}

// channelMonitorV3ProbeStatus picks the HTTP status out of account-test errors
// such as "API returned 503: ..." so the V2 taxonomy can classify them.
var channelMonitorV3ProbeStatus = regexp.MustCompile(`(?i)(?:returned|http)\s*\(?\s*([1-5]\d\d)\b`)

func channelMonitorV3ProbeCategory(message string) string {
	if strings.HasPrefix(message, channelMonitorV3WrongAnswerPrefix) {
		return ChannelMonitorV3ProbeWrongAnswer
	}
	input := ChannelMonitorV2ErrorInput{Message: message}
	if match := channelMonitorV3ProbeStatus.FindStringSubmatch(message); match != nil {
		input.UpstreamStatusCode, _ = strconv.Atoi(match[1])
	}
	return ClassifyChannelMonitorV2Error(input)
}

func channelMonitorV3Truncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit]) + "…"
}

type channelMonitorV3ProbeTarget struct {
	GroupID         int64
	Component       ChannelMonitorV3Component
	Model           string
	ReasoningEffort string
}

// channelMonitorV3ProbeTargets lists what the enabled components of the
// checked, usable groups would probe. A component covering every model asks
// the group's probe model and is skipped without one.
func channelMonitorV3ProbeTargets(probe ChannelMonitorV3IdleProbe, components []ChannelMonitorV3Component) []channelMonitorV3ProbeTarget {
	targets := []channelMonitorV3ProbeTarget{}
	for _, c := range components {
		i := slices.IndexFunc(probe.Groups, func(g ChannelMonitorV3IdleProbeGroup) bool { return g.GroupID == c.GroupID })
		if i < 0 || !c.Enabled || c.GroupDeleted || (c.GroupStatus != "" && c.GroupStatus != StatusActive) {
			continue
		}
		target := channelMonitorV3ProbeTarget{GroupID: c.GroupID, Component: c, Model: c.Model, ReasoningEffort: probe.Groups[i].ReasoningEffort}
		if target.Model == "" {
			target.Model = probe.Groups[i].Model
		}
		if target.Model != "" {
			targets = append(targets, target)
		}
	}
	return targets
}

// channelMonitorV3DueProbes keeps the targets whose components saw fewer real
// successes in the slot than the page minimum, one probe per group and model.
func channelMonitorV3DueProbes(targets []channelMonitorV3ProbeTarget, counts []ChannelMonitorV3Fact, minRequests int) []channelMonitorV3ProbeTarget {
	due := []channelMonitorV3ProbeTarget{}
	for _, t := range targets {
		var successes int64
		for _, c := range counts {
			if c.GroupID == t.GroupID && t.Component.includesModel(c.Model) {
				successes += c.Success
			}
		}
		if successes >= int64(minRequests) || slices.ContainsFunc(due, func(d channelMonitorV3ProbeTarget) bool { return d.GroupID == t.GroupID && d.Model == t.Model }) {
			continue
		}
		due = append(due, t)
	}
	return due
}
