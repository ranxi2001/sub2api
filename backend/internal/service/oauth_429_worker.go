package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const OAuth429ModelBlocksKey = "oauth_429_model_blocks"
const OAuth429Source = "upstream_429_independent"

// Independent reviews use the configured minimum number of business requests.
type OAuth429TestConfig struct {
	Enabled    bool `json:"enabled"`
	MinSamples int  `json:"min_samples"`
}

func (c OAuth429TestConfig) Valid() bool {
	return !c.Enabled || (c.MinSamples >= 20 && c.MinSamples <= 200)
}

type OAuth429Sample struct {
	Request string    `json:"request"`
	Status  int       `json:"status"`
	At      time.Time `json:"at"`
}
type OAuth429Run struct {
	ID             string    `json:"id"`
	ResultID       string    `json:"result_id,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
	Verdict        string    `json:"verdict"`
	HTTPStatus     int       `json:"http_status,omitempty"`
	AnswerComplete bool      `json:"answer_complete,omitempty"`
	Digest         string    `json:"digest,omitempty"`
}
type OAuth429Task struct {
	AccountID       int64            `json:"account_id"`
	AccountEpoch    int64            `json:"account_epoch"`
	Model           string           `json:"model"`
	Reason          string           `json:"reason,omitempty"`
	ModelBlocked    bool             `json:"model_blocked,omitempty"`
	RequestModel    string           `json:"request_model"`
	Endpoint        string           `json:"endpoint,omitempty"`
	RoutingRevision string           `json:"routing_revision,omitempty"`
	CompactModel    string           `json:"compact_model,omitempty"`
	Revision        string           `json:"revision"`
	TriggerID       string           `json:"trigger_id"`
	Generation      int64            `json:"generation"`
	TriggeredAt     time.Time        `json:"triggered_at"`
	ExpiresAt       time.Time        `json:"expires_at"`
	Lease           string           `json:"lease"`
	LeaseUntil      time.Time        `json:"lease_until"`
	State           string           `json:"state"`
	Armed           bool             `json:"armed"`
	Samples         []OAuth429Sample `json:"samples"`
	Runs            []OAuth429Run    `json:"runs"`
}

// The repository owns all transitions and live fences. No scheduled plan,
// quality history or quality action owner participates in this lifecycle.
type OAuth429Repository interface {
	ObserveOAuth429(context.Context, OAuth429Observation) (bool, error)
	ClaimOAuth429(context.Context) (*OAuth429Task, error)
	OAuth429Current(context.Context, *OAuth429Task) (bool, error)
	StartOAuth429Run(context.Context, *OAuth429Task) (*OAuth429Run, error)
	FinishOAuth429Run(context.Context, *OAuth429Task, OAuth429Run) error
	CompleteOAuth429(context.Context, *OAuth429Task) error
}

func isOAuth429Execution(ctx context.Context) bool {
	_, ok := ctx.Value(oauth429ExecutionKey{}).(*oauth429Execution)
	return ok
}

type oauth429ExecutionKey struct{}
type oauth429Execution struct {
	AccountID      int64
	Model          string
	Endpoint       string
	CompactModel   string
	httpStatus     atomic.Int32
	answerComplete atomic.Bool
}

// Only a terminal event with a normal finish reason certifies an answer.
// Other test transports can still report connectivity without grading a model.
func confirmOAuth429Answer(ctx context.Context, normal bool) {
	if run, ok := ctx.Value(oauth429ExecutionKey{}).(*oauth429Execution); ok && normal {
		run.answerComplete.Store(true)
	}
}

// ValidateOAuth429Upstream fences any hidden alternate-account/model route.
func ValidateOAuth429Upstream(req *http.Request, accountID int64) error {
	if req == nil {
		return nil
	}
	expected, ok := req.Context().Value(oauth429ExecutionKey{}).(*oauth429Execution)
	if !ok {
		return nil
	}
	if expected.AccountID != accountID || req.GetBody == nil {
		return errors.New("oauth429 target account changed")
	}
	body, err := req.GetBody()
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return err
	}
	if gjson.GetBytes(data, "model").String() != expected.Model {
		return errors.New("oauth429 target model changed")
	}
	return nil
}

// Keep only routing metadata in the bounded queues. Each account has a single
// FIFO consumer per gateway, including retried physical responses.
type OAuth429Observation struct {
	AccountID       int64
	Model           string
	Revision        string
	RequestModel    string
	Endpoint        string
	RoutingRevision string
	CompactModel    string
	// Request start fences a response spanning an account transition. At remains
	// the response-header timestamp used by the statistical window.
	StartedAt time.Time
	OAuth429Sample
}

var oauth429HTTPObserver atomic.Pointer[AccountOpsService]
var oauth429Wake = make(chan struct{}, 1)

func (s *AccountOpsService) startOAuth429Observations(ctx context.Context, repo OAuth429Repository) {
	if s.oauth429RoutingErr != nil {
		slog.Error("oauth429 routing initialization failed", "error", s.oauth429RoutingErr)
		return
	}
	for i := range s.oauth429Samples {
		queue := make(chan OAuth429Observation, 256)
		s.oauth429Samples[i] = queue
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case sample := <-queue:
					query, cancel := context.WithTimeout(ctx, 2*time.Second)
					queued, err := repo.ObserveOAuth429(query, sample)
					cancel()
					if err != nil {
						s.failures.Add(1)
					}
					if queued {
						select {
						case oauth429Wake <- struct{}{}:
						default:
						}
					}
				}
			}
		}()
	}
	oauth429HTTPObserver.Store(s)
}

func ObserveOAuth429HTTP(req *http.Request, accountID int64, status int, requestStarted ...time.Time) {
	observer := oauth429HTTPObserver.Load()
	if observer == nil || accountID <= 0 || req == nil || status < 200 || status > 599 || req.Context().Value(qualityProbeContextKey{}) != nil {
		return
	}
	cfg, _ := observer.autoConfig.Load().(OAuthAutoConfig)
	policy := cfg.DegradationAction
	if !policy.Enabled || !policy.UsesUpstream429() || !policy.Independent429.Enabled || cfg.Revision == "" {
		return
	}
	requestID, _ := req.Context().Value(ctxkey.ClientRequestID).(string)
	if requestID == "" {
		return
	}
	// Handlers already parsed the complete request, including large or streaming
	// bodies. Preserve their actual public model; the repository resolves and
	// validates its target under the live account mapping and epoch lock.
	requestModel, _ := req.Context().Value(ctxkey.Model).(string)
	model, _ := req.Context().Value(oauth429WireModelKey{}).(string)
	if model == "" && req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return
		}
		var meta struct {
			Model string `json:"model"`
		}
		err = json.NewDecoder(body).Decode(&meta)
		_ = body.Close()
		if err != nil {
			return
		}
		model = meta.Model
	}
	if model == "" && req.URL != nil {
		// Native Gemini identifies the model in its endpoint rather than JSON.
		if _, tail, ok := strings.Cut(req.URL.Path, "/models/"); ok {
			if value, _, ok := strings.Cut(tail, ":"); ok {
				model = value
			}
		}
	}
	if model == "" || len(requestModel) > 256 || len(model) > 256 {
		return
	}
	digest := sha256.Sum256([]byte(requestID))
	started := time.Now().UTC()
	if len(requestStarted) > 0 {
		started = requestStarted[0]
	}
	sample := OAuth429Observation{StartedAt: started, RequestModel: requestModel, AccountID: accountID, Model: model, Revision: cfg.Revision, OAuth429Sample: OAuth429Sample{
		Request: hex.EncodeToString(digest[:]), Status: status, At: time.Now().UTC(),
	}}
	if req.URL != nil {
		sample.Endpoint = req.URL.Path
	}
	sample.RoutingRevision = observer.oauth429Routing.Revision
	sample.CompactModel = oauth429CompactModel(req.Context())
	select {
	case observer.oauth429Samples[accountID%int64(len(observer.oauth429Samples))] <- sample:
	default:
		observer.dropped.Add(1)
	}
}

func (a *Account) IsOAuth429ModelBlocked(model string) bool {
	return a.isOAuth429ModelBlockedForEndpoint(model, "")
}

func (a *Account) isOAuth429ModelBlockedForEndpoint(model, endpoint string, compactModel ...string) bool {
	if a == nil {
		return false
	}
	entries, _ := a.Extra[OAuth429ModelBlocksKey].(map[string]any)
	return len(entries) > 0 && entries[ResolveOAuth429TargetModel(a, model, endpoint, compactModel...)] != nil
}

const OAuth429ConsecutiveLimit = 5

func (task *OAuth429Task) consecutiveRateLimits() int {
	count := 0
	for i := len(task.Runs) - 1; i >= 0; i-- {
		run := task.Runs[i]
		if run.Verdict != "rate_limited" || run.HTTPStatus != http.StatusTooManyRequests {
			break
		}
		count++
	}
	return count
}

func (task *OAuth429Task) NeedsAnotherRun() bool {
	if len(task.Runs) < 2 {
		return true
	}
	streak := task.consecutiveRateLimits()
	return streak > 0 && streak < OAuth429ConsecutiveLimit && len(task.Runs) < OAuth429ConsecutiveLimit+1
}

// A result may authorize action only while its original trigger and lease are
// current. Require distinct, chronologically ordered run/result pairs.
func (task *OAuth429Task) DecisionReason() string {
	if len(task.Runs) < 2 || len(task.Runs) > OAuth429ConsecutiveLimit+1 {
		return ""
	}
	ids := make(map[string]bool)
	last := task.TriggeredAt
	for _, run := range task.Runs {
		if run.ID == "" || run.ResultID == "" || run.ID == run.ResultID || ids[run.ID] || ids[run.ResultID] ||
			run.StartedAt.Before(last) || run.FinishedAt.Before(run.StartedAt) || run.FinishedAt.After(task.ExpiresAt) {
			return ""
		}
		ids[run.ID], ids[run.ResultID] = true, true
		last = run.FinishedAt
	}
	if task.consecutiveRateLimits() == OAuth429ConsecutiveLimit {
		return "five_consecutive_429"
	}
	one, two := task.Runs[0], task.Runs[1]
	if len(task.Runs) == 2 && one.AnswerComplete && two.AnswerComplete && one.Verdict == "incorrect" && two.Verdict == "incorrect" {
		return "two_wrong_answers"
	}
	return ""
}

// Capture the final inference HTTP status under the target identity check.
// A number in an answer, a transport error, or a local admission rejection
// cannot supply this evidence.
func recordOAuth429TestStatus(req *http.Request, accountID int64, resp *http.Response, err error) {
	run, ok := req.Context().Value(oauth429ExecutionKey{}).(*oauth429Execution)
	if !ok {
		return
	}
	run.httpStatus.Store(0)
	if err == nil && resp != nil && req.Context().Err() == nil && ValidateOAuth429Upstream(req, accountID) == nil {
		run.httpStatus.Store(int32(resp.StatusCode))
	}
}

// RunOAuth429Task starts with two calls. A crash after dispatch
// leaves an inconclusive slot, never permission to replay that billable call.
func (s *ScheduledTestRunnerService) RunOAuth429Task(ctx context.Context, repo OAuth429Repository, task *OAuth429Task) error {
	ctx, cancel := context.WithDeadline(ctx, task.ExpiresAt)
	go func() {
		timer := time.NewTicker(100 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				valid, err := repo.OAuth429Current(ctx, task)
				if err != nil || !valid {
					cancel()
					return
				}
			}
		}
	}()
	defer cancel()
	for task.NeedsAnotherRun() {
		account, err := s.accountTestSvc.accountRepo.GetByID(ctx, task.AccountID)
		if err != nil {
			return err
		}
		if !account.Schedulable || account.Status != StatusActive {
			return repo.CompleteOAuth429(ctx, task)
		}
		// Use the same account concurrency admission as scheduled/group tests.
		var slot *AcquireResult
		for {
			slot, err = s.oauth429Slots.AcquireAccountSlot(ctx, account.ID, account.Concurrency)
			if err != nil {
				return err
			}
			if slot != nil && slot.Acquired {
				break
			}
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		run, err := repo.StartOAuth429Run(ctx, task)
		if err != nil || run == nil {
			if slot.ReleaseFunc != nil {
				slot.ReleaseFunc()
			}
			if err != nil {
				return err
			}
			return repo.CompleteOAuth429(ctx, task)
		}
		cfg := &PelicanTestConfig{QuestionKind: "candy", Prompt: CandyPrompt + "\n你可以凭手感选择每种形状取多少颗，不能选择口味。", ReasoningEffort: "high", ParallelCount: 1, ModelID: task.RequestModel, Quality: &QualityPolicy{Action: QualityActionObserveOnly}}
		execution := &oauth429Execution{AccountID: task.AccountID, Model: task.Model, Endpoint: task.Endpoint, CompactModel: task.CompactModel}
		testCtx, stop := context.WithTimeout(context.WithValue(ctx, oauth429ExecutionKey{}, execution), 90*time.Second)
		// This direct executor addresses the original account; no group selection,
		// answer retry, scheduled plan, or qualityops switch is consulted.
		result, executeErr := s.runOAuth429Answer(testCtx, task, cfg)
		run.AnswerComplete = execution.answerComplete.Load()
		run.Verdict = oauth429Verdict(result, executeErr, testCtx.Err(), task.RequestModel, run.AnswerComplete)
		run.HTTPStatus = int(execution.httpStatus.Load())
		if run.HTTPStatus == http.StatusTooManyRequests && testCtx.Err() == nil {
			run.Verdict = "rate_limited"
		}
		run.ResultID, run.FinishedAt = uuid.NewString(), time.Now().UTC()
		if result != nil {
			digest := sha256.Sum256([]byte(result.ResponseText))
			run.Digest = hex.EncodeToString(digest[:])
		}
		stop()
		if slot.ReleaseFunc != nil {
			slot.ReleaseFunc()
		}
		if err := repo.FinishOAuth429Run(ctx, task, *run); err != nil {
			return err
		}
		task.Runs = append(task.Runs, *run)
	}
	return repo.CompleteOAuth429(ctx, task)
}

func (s *ScheduledTestRunnerService) runOAuth429Answer(ctx context.Context, task *OAuth429Task, cfg *PelicanTestConfig) (result *ScheduledTestResult, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = errors.New("independent answer executor interrupted")
		}
	}()
	return s.runPelican(ctx, task.AccountID, task.RequestModel, cfg)
}

var oauth429Integer = regexp.MustCompile(`^[0-9]{1,6}$`)

func oauth429Verdict(result *ScheduledTestResult, err, contextErr error, model string, complete bool) string {
	if !complete || err != nil || contextErr != nil || result == nil || result.Status != "success" || result.ErrorMessage != "" || result.PelicanConfig == nil || result.PelicanConfig.ModelID != model {
		return "inconclusive"
	}
	answer := strings.TrimSpace(result.ResponseText)
	if CandyAnswerCorrect(answer) {
		return "correct"
	}
	if !oauth429Integer.MatchString(answer) {
		return "inconclusive"
	}
	number := strings.TrimLeft(answer, "0")
	// Relays may stream a bare HTTP error code inside a successful SSE response.
	if len(number) == 3 && number >= "400" && number <= "599" {
		return "inconclusive"
	}
	if number == "21" {
		return "correct"
	}
	return "incorrect"
}

func (s *ScheduledTestRunnerService) StartOAuth429Worker() {
	s.oauth429Start.Do(s.startOAuth429)
}

func (s *ScheduledTestRunnerService) startOAuth429() {
	if s.oauth429Repo == nil || s.oauth429Slots == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.oauth429Cancel = cancel
	s.oauth429WG.Add(1)
	go func() {
		defer s.oauth429WG.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var workers sync.WaitGroup
		defer workers.Wait()
		for {
			// A local threshold signal wakes execution immediately; replicas/restarts
			// recover from the persistent queue without waiting for a quality cron.
			for ctx.Err() == nil {
				select {
				case s.planSlots <- struct{}{}:
				default:
					goto wait
				}
				task, err := s.oauth429Repo.ClaimOAuth429(ctx)
				if err != nil || task == nil {
					<-s.planSlots
					break
				}
				workers.Add(1)
				go func(task *OAuth429Task) {
					defer workers.Done()
					defer func() { <-s.planSlots }()
					if err := s.RunOAuth429Task(ctx, s.oauth429Repo, task); err != nil {
						slog.Warn("oauth429 task failed", "trigger_id", task.TriggerID, "error", err)
					}
				}(task)
			}
		wait:
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-oauth429Wake:
			}
		}
	}()
}
