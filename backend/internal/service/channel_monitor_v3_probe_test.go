package service

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorV3ProbesJudgeOnlyQuietSlots(t *testing.T) {
	cfg := monitorV3Config()
	cfg.MinRequests = 3
	component := ChannelMonitorV3Component{ID: 1, GroupID: 4}
	first := monitorV3Slot(7)
	slot := func(i int) time.Time { return first.Add(time.Duration(i) * 5 * time.Minute) }
	probe := func(i int, category string, latency int64) ChannelMonitorV3ProbeFact {
		return ChannelMonitorV3ProbeFact{GroupID: 4, Model: "gpt-probe", Slot: slot(i), Category: category, Count: 1, LatencyP50Ms: latency}
	}
	facts := &ChannelMonitorV3Facts{
		Metrics: []ChannelMonitorV3Fact{
			{GroupID: 4, Model: "gpt", Slot: slot(0), Success: 10},
			{GroupID: 4, Model: "gpt", Slot: slot(5), Success: 1},
			{GroupID: 4, Model: "gpt", Slot: slot(7), Success: 2},
		},
		Probes: []ChannelMonitorV3ProbeFact{
			probe(0, "upstream_5xx", 0), // real traffic was enough; the probe is ignored
			probe(1, "", 2000),
			probe(2, "", 12000),
			probe(3, "upstream_5xx", 0),
			probe(4, "model_unsupported", 0), // the probe asked for something the group does not serve
			probe(5, "", 1500),               // one real request is below the minimum
			probe(6, "authentication", 0),    // user-error filters do not cover probes
		},
	}
	cells := channelMonitorV3Cells(cfg, component, facts, first, 8, 5*time.Minute, true)
	statuses := make([]string, len(cells))
	for i, cell := range cells {
		if cell != nil {
			statuses[i] = cell.Status
		}
	}
	require.Equal(t, []string{
		ChannelMonitorV3StatusOperational,
		ChannelMonitorV3StatusOperational,
		ChannelMonitorV3StatusDegraded,
		ChannelMonitorV3StatusDown,
		"",
		ChannelMonitorV3StatusOperational,
		ChannelMonitorV3StatusDown,
		ChannelMonitorV3StatusInsufficient,
	}, statuses)
	require.False(t, cells[0].Probed)
	require.Equal(t, int64(10), cells[0].Requests)
	require.Equal(t, int64(1), cells[0].Probes, "admins still see that a probe ran")
	require.True(t, cells[1].Probed)
	require.Equal(t, int64(2000), *cells[1].ProbeLatencyMs)
	require.Nil(t, cells[1].TTFTP50Ms)
	require.InDelta(t, 1.0, cells[1].SuccessRate, 1e-9)
	require.Equal(t, "upstream_5xx", cells[3].TopError)
	require.Zero(t, cells[3].SuccessRate)
	require.Equal(t, int64(1), cells[3].ProbeErrors)
	require.True(t, cells[5].Probed)
	require.Equal(t, int64(1), cells[5].Requests)
	require.Equal(t, "authentication", cells[6].TopError)

	// The component threshold applies to probe latency too.
	component.DegradedTTFTMs = 15000
	require.Equal(t, ChannelMonitorV3StatusOperational, channelMonitorV3Cells(cfg, component, facts, first, 8, 5*time.Minute, true)[2].Status)
	// Users learn a slot was probed, never the volumes.
	userCell := channelMonitorV3Cells(cfg, component, facts, first, 8, 5*time.Minute, false)[3]
	require.True(t, userCell.Probed)
	require.Zero(t, userCell.Probes)
	require.Zero(t, userCell.ProbeErrors)
}

func TestChannelMonitorV3ProbesFollowTheComponentModel(t *testing.T) {
	cfg := monitorV3Config()
	first := monitorV3Slot(0)
	facts := &ChannelMonitorV3Facts{Probes: []ChannelMonitorV3ProbeFact{
		{GroupID: 4, Model: "gpt-a", Slot: first, Count: 1, LatencyP50Ms: 1000},
		{GroupID: 4, Model: "gpt-b", Slot: first, Category: "timeout", Count: 1},
	}}
	whole := channelMonitorV3Cells(cfg, ChannelMonitorV3Component{GroupID: 4}, facts, first, 1, 5*time.Minute, true)[0]
	require.Equal(t, ChannelMonitorV3StatusDown, whole.Status, "half the group's probes failed")
	require.Equal(t, int64(2), whole.Probes)
	one := channelMonitorV3Cells(cfg, ChannelMonitorV3Component{GroupID: 4, Model: "gpt-a"}, facts, first, 1, 5*time.Minute, true)[0]
	require.Equal(t, ChannelMonitorV3StatusOperational, one.Status)
	require.Equal(t, int64(1), one.Probes)
}

func monitorV3ProbeStatusFixture() *monitorV3RepoFake {
	repo := monitorV3StatusFixture()
	repo.config.IdleProbe = ChannelMonitorV3IdleProbe{Enabled: true, Groups: []ChannelMonitorV3IdleProbeGroup{{GroupID: 4, Model: "gpt"}}}
	// Group 4 has no real traffic in slot 2; group 5 is not checked.
	repo.probeSlots = []ChannelMonitorV3ProbeFact{
		{GroupID: 4, Model: "gpt", Slot: monitorV3Slot(2), Category: "timeout", Count: 1},
		{GroupID: 5, Model: "claude", Slot: monitorV3Slot(2), Count: 1, LatencyP50Ms: 900},
	}
	repo.probeTotals = []ChannelMonitorV3ProbeFact{{GroupID: 4, Model: "gpt", Count: 150, LatencyP50Ms: 900}}
	return repo
}

func TestChannelMonitorV3StatusReadsProbesOfCheckedGroups(t *testing.T) {
	repo := monitorV3ProbeStatusFixture()
	status, err := monitorV3Service(repo).Status(context.Background(), ChannelMonitorV3Viewer{Admin: true}, nil)
	require.NoError(t, err)
	require.True(t, status.IdleProbe)
	for _, call := range repo.probeCalls {
		require.Equal(t, []int64{4}, call.groups, "only checked groups are read")
	}
	require.Len(t, repo.probeCalls, 2, "slots and availability totals")
	require.Zero(t, repo.probeCalls[1].interval)
	codex := status.Categories[1].Components[0]
	require.Equal(t, "Codex", codex.Name)
	require.True(t, codex.Cells[27].Probed)
	require.Equal(t, ChannelMonitorV3StatusDown, codex.Cells[27].Status)
	// Totals: 800 answered of 1000 counted real requests (50 cancellations are
	// not counted), plus 150 successful probes.
	require.InDelta(t, 950.0*100/1150, *codex.Availability, 1e-9)
	require.Equal(t, int64(150), codex.Probes)
	public := status.Categories[1].Components[1]
	require.Nil(t, public.Cells[27], "probes of unchecked groups are not shown")

	// Turning the switch off brings the page back to real traffic only.
	repo.config.IdleProbe.Enabled = false
	repo.probeCalls = nil
	status, err = monitorV3Service(repo).Status(context.Background(), ChannelMonitorV3Viewer{Admin: true}, nil)
	require.NoError(t, err)
	require.False(t, status.IdleProbe)
	require.Empty(t, repo.probeCalls)
	require.Nil(t, status.Categories[1].Components[0].Cells[27])
}

func TestChannelMonitorV3ProbeFailuresBecomeIncidents(t *testing.T) {
	repo := &monitorV3RepoFake{config: monitorV3Config()}
	repo.config.IdleProbe = ChannelMonitorV3IdleProbe{Enabled: true, Groups: []ChannelMonitorV3IdleProbeGroup{{GroupID: 4, Model: "gpt"}}}
	repo.components = []ChannelMonitorV3Component{{ID: 10, Name: "Quiet", GroupID: 4, Enabled: true, Visibility: ChannelMonitorV3VisibilityPublic}}
	for _, back := range []int{4, 3, 1} {
		repo.probeSlots = append(repo.probeSlots, ChannelMonitorV3ProbeFact{GroupID: 4, Model: "gpt", Slot: monitorV3Slot(back), Category: "upstream_5xx", Count: 1})
	}
	repo.probeSlots = append(repo.probeSlots, ChannelMonitorV3ProbeFact{GroupID: 4, Model: "gpt", Slot: monitorV3Slot(2), Count: 1, LatencyP50Ms: 800})
	s := monitorV3Service(repo)
	page, err := s.Incidents(context.Background(), ChannelMonitorV3Viewer{Admin: true}, 1, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 1, "one probed red slot alone is a blip")
	require.Equal(t, monitorV3Slot(4), page.Items[0].StartedAt)
	require.Equal(t, monitorV3Slot(2), *page.Items[0].EndedAt)
	require.Equal(t, int64(2), page.Items[0].Probes)
	require.Equal(t, "upstream_5xx", page.Items[0].TopError)
	page, err = s.Incidents(context.Background(), ChannelMonitorV3Viewer{AllowedGroups: map[int64]bool{4: true}}, 1, 20)
	require.NoError(t, err)
	require.Zero(t, page.Items[0].Probes, "users never get volumes")
}

func TestChannelMonitorV3UpdateIdleProbe(t *testing.T) {
	repo := &monitorV3RepoFake{config: monitorV3Config()}
	repo.components = []ChannelMonitorV3Component{
		{ID: 1, GroupID: 4, Enabled: true},
		{ID: 2, GroupID: 5, Model: "claude-opus-5-5", Enabled: true},
	}
	s := monitorV3Service(repo)
	ctx := context.Background()
	for name, input := range map[string]ChannelMonitorV3IdleProbe{
		"duplicate":       {Groups: []ChannelMonitorV3IdleProbeGroup{{GroupID: 5}, {GroupID: 5}}},
		"no group":        {Groups: []ChannelMonitorV3IdleProbeGroup{{GroupID: 0}}},
		"unknown group":   {Groups: []ChannelMonitorV3IdleProbeGroup{{GroupID: 99, Model: "x"}}},
		"bad effort":      {Groups: []ChannelMonitorV3IdleProbeGroup{{GroupID: 5, ReasoningEffort: "turbo"}}},
		"long model":      {Groups: []ChannelMonitorV3IdleProbeGroup{{GroupID: 4, Model: string(make([]rune, 101))}}},
		"model for whole": {Enabled: true, Groups: []ChannelMonitorV3IdleProbeGroup{{GroupID: 4}}},
	} {
		_, err := s.UpdateIdleProbe(ctx, input)
		require.ErrorIs(t, err, ErrChannelMonitorV3Invalid, name)
	}
	saved, err := s.UpdateIdleProbe(ctx, ChannelMonitorV3IdleProbe{Enabled: true, Groups: []ChannelMonitorV3IdleProbeGroup{
		{GroupID: 4, Model: " gpt-5.5 ", ReasoningEffort: " LOW "},
		{GroupID: 5}, // its component names the model to probe
	}})
	require.NoError(t, err)
	want := ChannelMonitorV3IdleProbe{Enabled: true, Groups: []ChannelMonitorV3IdleProbeGroup{{GroupID: 4, Model: "gpt-5.5", ReasoningEffort: "low"}, {GroupID: 5}}}
	require.Equal(t, want, *saved)
	require.Equal(t, want, repo.config.IdleProbe)
	require.Equal(t, 1, repo.config.Version, "the page config version is left alone")

	settings, err := s.Settings(ctx)
	require.NoError(t, err)
	require.Equal(t, want, settings.IdleProbe)
	require.NotNil(t, settings.IdleProbeStats)
}

// monitorV3ProbeTick is 30 seconds before the five-minute slot that started at 09:30 ends.
var monitorV3ProbeTick = time.Date(2026, 10, 11, 9, 34, 30, 0, time.UTC)

var monitorV3ChallengePattern = regexp.MustCompile(`(\d+) ([+-]) (\d+) = \?\nA:$`)

// monitorV3Answer solves the V1 challenge in a probe prompt.
func monitorV3Answer(prompt string) string {
	match := monitorV3ChallengePattern.FindStringSubmatch(prompt)
	a, _ := strconv.Atoi(match[1])
	b, _ := strconv.Atoi(match[3])
	if match[2] == "-" {
		return strconv.Itoa(a - b)
	}
	return strconv.Itoa(a + b)
}

type monitorV3AccountRun func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error)

func monitorV3ProberFixture(router *groupTestRouterFake, run monitorV3AccountRun) (*ChannelMonitorV3Prober, *monitorV3RepoFake, *PelicanGroupTestService) {
	repo := &monitorV3RepoFake{config: monitorV3Config()}
	repo.config.IdleProbe = ChannelMonitorV3IdleProbe{Enabled: true, Groups: []ChannelMonitorV3IdleProbeGroup{{GroupID: 4, Model: "gpt-probe", ReasoningEffort: "low"}}}
	repo.components = []ChannelMonitorV3Component{
		{ID: 1, Name: "Whole group", GroupID: 4, Enabled: true, GroupStatus: StatusActive},
		{ID: 2, Name: "Unchecked", GroupID: 5, Enabled: true, GroupStatus: StatusActive},
	}
	groups, _ := newGroupTestService(newGroupTestRepoFake(), router, run)
	p := NewChannelMonitorV3Prober(repo, groups, channelMonitorV2RuntimeStub{rt: ChannelMonitorRuntime{Enabled: true, Mode: ChannelMonitorModeV3}})
	p.now = func() time.Time { return monitorV3ProbeTick }
	return p, repo, groups
}

func monitorV3Claims(repo *monitorV3RepoFake) []string {
	keys := []string{}
	for key := range repo.claims {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func TestChannelMonitorV3ProberProbesQuietSlotsOnce(t *testing.T) {
	asked := []*PelicanTestConfig{}
	router := &groupTestRouterFake{steps: []routeStep{{account: account(77, "scheduler choice")}}}
	p, repo, _ := monitorV3ProberFixture(router, func(_ context.Context, id int64, model string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
		require.Equal(t, int64(77), id)
		require.Equal(t, "gpt-probe", model)
		asked = append(asked, cfg)
		return &ScheduledTestResult{Status: "success", ResponseText: monitorV3Answer(cfg.Prompt)}, nil
	})
	p.RunDue(context.Background(), monitorV3ProbeTick)

	require.Equal(t, []string{"4/gpt-probe/2026-10-11T09:30:00Z/5"}, monitorV3Claims(repo))
	require.Len(t, repo.finished, 1)
	require.Equal(t, ChannelMonitorV3ProbeSuccess, repo.finished[0].Outcome)
	require.Empty(t, repo.finished[0].Category)
	require.Equal(t, int64(77), repo.finished[0].AccountID)
	require.Len(t, asked, 1)
	require.Equal(t, ChannelMonitorV3ProbeQuestionKind, asked[0].QuestionKind)
	require.Equal(t, "low", asked[0].ReasoningEffort)
	require.Contains(t, asked[0].Prompt, "Calculate and respond with ONLY the number")
	require.Equal(t, []monitorV3SlotCall{{groups: []int64{4}, from: time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC), to: monitorV3ProbeTick}}, repo.countCalls)
	require.Equal(t, []int64{77}, router.released)

	// The claim keeps a slot from being probed twice, also across instances.
	p.RunDue(context.Background(), monitorV3ProbeTick)
	require.Len(t, repo.finished, 1)
	require.Len(t, asked, 1)
}

func TestChannelMonitorV3ProberGates(t *testing.T) {
	for name, mutate := range map[string]func(*ChannelMonitorV3Prober, *monitorV3RepoFake, *PelicanGroupTestService){
		"not the last minute": func(p *ChannelMonitorV3Prober, _ *monitorV3RepoFake, _ *PelicanGroupTestService) {
			p.now = func() time.Time { return monitorV3ProbeTick.Add(-2 * time.Minute) }
		},
		"enough real traffic": func(_ *ChannelMonitorV3Prober, r *monitorV3RepoFake, _ *PelicanGroupTestService) {
			r.successCounts = []ChannelMonitorV3Fact{{GroupID: 4, Model: "gpt-5.5", Success: 1}}
		},
		"switched off": func(_ *ChannelMonitorV3Prober, r *monitorV3RepoFake, _ *PelicanGroupTestService) {
			r.config.IdleProbe.Enabled = false
		},
		"group unchecked": func(_ *ChannelMonitorV3Prober, r *monitorV3RepoFake, _ *PelicanGroupTestService) {
			r.config.IdleProbe.Groups[0].GroupID = 5
		},
		"no model to ask": func(_ *ChannelMonitorV3Prober, r *monitorV3RepoFake, _ *PelicanGroupTestService) {
			r.config.IdleProbe.Groups[0].Model = ""
		},
		"site shows v2": func(p *ChannelMonitorV3Prober, _ *monitorV3RepoFake, _ *PelicanGroupTestService) {
			p.settings = channelMonitorV2RuntimeStub{rt: ChannelMonitorRuntime{Enabled: true, Mode: ChannelMonitorModeV2}}
		},
		"monitor off": func(p *ChannelMonitorV3Prober, _ *monitorV3RepoFake, _ *PelicanGroupTestService) {
			p.settings = channelMonitorV2RuntimeStub{rt: ChannelMonitorRuntime{Enabled: false, Mode: ChannelMonitorModeV3}}
		},
		"component off": func(_ *ChannelMonitorV3Prober, r *monitorV3RepoFake, _ *PelicanGroupTestService) {
			r.components[0].Enabled = false
		},
		"group disabled": func(_ *ChannelMonitorV3Prober, r *monitorV3RepoFake, _ *PelicanGroupTestService) {
			r.components[0].GroupStatus = "disabled"
		},
		"group deleted": func(_ *ChannelMonitorV3Prober, r *monitorV3RepoFake, _ *PelicanGroupTestService) {
			r.components[0].GroupDeleted = true
		},
		"group turned off since listing": func(_ *ChannelMonitorV3Prober, _ *monitorV3RepoFake, g *PelicanGroupTestService) {
			g.groups = groupTestGroupsFake{4: {ID: 4, Status: "disabled"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, repo, groups := monitorV3ProberFixture(&groupTestRouterFake{steps: []routeStep{{account: account(77, "a")}}}, func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
				t.Fatal("no probe expected")
				return nil, nil
			})
			mutate(p, repo, groups)
			p.RunDue(context.Background(), p.now())
			require.Empty(t, repo.claims)
			require.Len(t, repo.prunedBefore, 1, "old probes are pruned whatever the switch")
		})
	}
}

func TestChannelMonitorV3ProberCountsOnlyTheComponentModel(t *testing.T) {
	probed := 0
	p, repo, _ := monitorV3ProberFixture(&groupTestRouterFake{steps: []routeStep{{account: account(77, "a")}, {account: account(78, "b")}}},
		func(_ context.Context, _ int64, model string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
			require.Equal(t, "gpt-a", model, "a component narrowed to a model probes that model")
			probed++
			return &ScheduledTestResult{Status: "success", ResponseText: monitorV3Answer(cfg.Prompt)}, nil
		})
	repo.components = []ChannelMonitorV3Component{{ID: 1, GroupID: 4, Model: "gpt-a", Enabled: true}}
	repo.successCounts = []ChannelMonitorV3Fact{{GroupID: 4, Model: "gpt-b", Success: 9}}
	p.RunDue(context.Background(), monitorV3ProbeTick)
	require.Equal(t, 1, probed, "traffic of another model does not make this one busy")

	repo.claims = nil
	repo.successCounts = []ChannelMonitorV3Fact{{GroupID: 4, Model: "gpt-a", Success: 1}}
	p.RunDue(context.Background(), monitorV3ProbeTick)
	require.Equal(t, 1, probed)
}

func TestChannelMonitorV3SelectedModels(t *testing.T) {
	input := ChannelMonitorV3ComponentInput{Name: "text", GroupID: 4, Models: []string{" sol ", "astra", "sol"}}
	require.NoError(t, normalizeChannelMonitorV3Component(&input))
	require.Equal(t, []string{"sol", "astra"}, input.Models)
	require.Equal(t, "sol", input.Model)
	component := ChannelMonitorV3Component{GroupID: 4, Model: input.Model, Models: input.Models, Enabled: true}
	facts := ChannelMonitorV3Facts{Metrics: []ChannelMonitorV3Fact{
		{GroupID: 4, Model: "sol", Success: 9},
		{GroupID: 4, Model: "astra", Success: 3, Errors: 1},
		{GroupID: 4, Model: "terra", Errors: 300},
		{GroupID: 5, Model: "sol", Errors: 100},
	}}
	total := channelMonitorV3Accumulate(ChannelMonitorV3Config{}, component, &facts, time.Time{}, 0)[0]
	require.EqualValues(t, 12, total.success)
	require.EqualValues(t, 1, total.errors)
	probe := ChannelMonitorV3IdleProbe{Groups: []ChannelMonitorV3IdleProbeGroup{{GroupID: 4}}}
	targets := channelMonitorV3ProbeTargets(probe, []ChannelMonitorV3Component{component})
	require.Len(t, targets, 1)
	require.Equal(t, "sol", targets[0].Model)
	require.Empty(t, channelMonitorV3DueProbes(targets, facts.Metrics, 10))
}

func TestChannelMonitorV3ProberVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		steps    []routeStep
		reply    func(cfg *PelicanTestConfig) *ScheduledTestResult
		category string
	}{
		{"wrong answer", []routeStep{{account: account(77, "a")}}, func(cfg *PelicanTestConfig) *ScheduledTestResult {
			wrong, _ := strconv.Atoi(monitorV3Answer(cfg.Prompt))
			return &ScheduledTestResult{Status: "success", ResponseText: strconv.Itoa(wrong + 1)}
		}, ChannelMonitorV3ProbeWrongAnswer},
		{"upstream 5xx", []routeStep{{account: account(77, "a")}}, func(*PelicanTestConfig) *ScheduledTestResult {
			return &ScheduledTestResult{Status: "failed", ResponseText: "", ErrorMessage: "API returned 503: upstream unavailable"}
		}, "upstream_5xx"},
		{"expired upstream login", []routeStep{{account: account(77, "a")}}, func(*PelicanTestConfig) *ScheduledTestResult {
			return &ScheduledTestResult{Status: "failed", ErrorMessage: "API returned 401: token expired"}
		}, "authentication"},
		{"no account", nil, nil, "account_pool_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, repo, _ := monitorV3ProberFixture(&groupTestRouterFake{steps: tc.steps}, func(_ context.Context, _ int64, _ string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
				return tc.reply(cfg), nil
			})
			p.RunDue(context.Background(), monitorV3ProbeTick)
			require.Len(t, repo.finished, 1)
			require.Equal(t, ChannelMonitorV3ProbeFailure, repo.finished[0].Outcome)
			require.Equal(t, tc.category, repo.finished[0].Category, repo.finished[0].ErrorMessage)
			require.NotEmpty(t, repo.finished[0].ErrorMessage)
		})
	}
}

func TestChannelMonitorV3ProberAbandonsProbesOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, repo, _ := monitorV3ProberFixture(&groupTestRouterFake{steps: []routeStep{{account: account(77, "a")}}}, func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
		cancel()
		return &ScheduledTestResult{Status: "failed", ErrorMessage: "context canceled"}, nil
	})
	p.RunDue(ctx, monitorV3ProbeTick)
	require.Len(t, repo.claims, 1)
	require.Empty(t, repo.finished, "an interrupted probe must not paint the slot red")
}

func TestChannelMonitorV3ProberPrunesHourlyByRange(t *testing.T) {
	p, repo, _ := monitorV3ProberFixture(&groupTestRouterFake{}, nil)
	repo.config.IdleProbe.Enabled = false
	p.RunDue(context.Background(), monitorV3ProbeTick)
	p.RunDue(context.Background(), monitorV3ProbeTick.Add(30*time.Minute))
	require.Equal(t, []time.Time{monitorV3ProbeTick.Add(-8 * 24 * time.Hour)}, repo.prunedBefore)

	repo.config.AvailabilityRange = "30d"
	p.RunDue(context.Background(), monitorV3ProbeTick.Add(time.Hour))
	require.Equal(t, monitorV3ProbeTick.Add(time.Hour-31*24*time.Hour), repo.prunedBefore[1])
	require.Equal(t, 8*24*time.Hour, channelMonitorV3ProbeRetention("24h"), "bars still page back seven days")
}

func TestChannelMonitorV3ProbeCategories(t *testing.T) {
	for message, want := range map[string]string{
		"answer_mismatch: expected 12, got \"13\"":     ChannelMonitorV3ProbeWrongAnswer,
		"API returned 502: bad gateway":                "upstream_5xx",
		"Grok Realtime WS handshake failed (HTTP 429)": "rate_or_capacity",
		"API returned 404: The model does not exist":   "not_found",
		"timeout: no answer within 90 seconds":         "timeout",
		"no_available_account: no available accounts":  "account_pool_unavailable",
		"probe_failed: no result":                      "other",
	} {
		require.Equal(t, want, channelMonitorV3ProbeCategory(message), message)
	}
	require.True(t, channelMonitorV3ProbeIgnored("not_found"))
	require.False(t, channelMonitorV3ProbeIgnored("authentication"))
}

func TestPelicanProbeKindAsksOnlyTheChallenge(t *testing.T) {
	cfg := &PelicanTestConfig{QuestionKind: ChannelMonitorV3ProbeQuestionKind, Prompt: "Q: 1 + 2 = ?\nA:"}
	require.Equal(t, "Q: 1 + 2 = ?\nA:", intelligenceTestPrompt(cfg), "no HTML or integer contract is appended")
	require.Empty(t, intelligenceTestOutputError(cfg, "3"), "the answer is not required to be HTML")
	require.Equal(t, pelicanErrEmptyOutput, intelligenceTestOutputError(cfg, " "))
}
