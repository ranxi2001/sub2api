package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Zero values never authorize paid work. The administrator must opt in and
// explicitly choose the cluster-wide daily call budget and minimum sample size.
type OAuth429TestConfig struct {
	Enabled         bool `json:"enabled"`
	DailyTestBudget int  `json:"daily_test_budget"`
	MinSamples      int  `json:"min_samples"`
}

func (c OAuth429TestConfig) Valid() bool {
	return !c.Enabled || (c.DailyTestBudget >= 2 && c.DailyTestBudget <= 1000 && c.MinSamples >= 20 && c.MinSamples <= 200)
}

type OAuth429Sample struct {
	Request string    `json:"request"`
	Status  int       `json:"status"`
	At      time.Time `json:"at"`
}
type OAuth429Run struct {
	ID         string    `json:"id"`
	ResultID   string    `json:"result_id,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Verdict    string    `json:"verdict"`
	Digest     string    `json:"digest,omitempty"`
}
type OAuth429Task struct {
	AccountID   int64            `json:"account_id"`
	Model       string           `json:"model"`
	Revision    string           `json:"revision"`
	TriggerID   string           `json:"trigger_id"`
	Generation  int64            `json:"generation"`
	TriggeredAt time.Time        `json:"triggered_at"`
	ExpiresAt   time.Time        `json:"expires_at"`
	Lease       string           `json:"lease"`
	LeaseUntil  time.Time        `json:"lease_until"`
	State       string           `json:"state"`
	Armed       bool             `json:"armed"`
	Samples     []OAuth429Sample `json:"samples"`
	Runs        []OAuth429Run    `json:"runs"`
}

// The repository owns all transitions and live fences. No scheduled plan,
// quality history or quality action owner participates in this lifecycle.
type OAuth429Repository interface {
	ObserveOAuth429(context.Context, int64, string, string, int) (bool, error)
	ClaimOAuth429(context.Context) (*OAuth429Task, error)
	StartOAuth429Run(context.Context, *OAuth429Task) (*OAuth429Run, error)
	FinishOAuth429Run(context.Context, *OAuth429Task, OAuth429Run) error
	CompleteOAuth429(context.Context, *OAuth429Task) error
}

type oauth429ExecutionKey struct{}
type oauth429Execution struct {
	AccountID int64
	Model     string
}

// ValidateOAuth429Upstream fences any hidden alternate-account/model route.
func ValidateOAuth429Upstream(req *http.Request, accountID int64) error {
	if req == nil {
		return nil
	}
	expected, ok := req.Context().Value(oauth429ExecutionKey{}).(oauth429Execution)
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
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return err
	}
	if gjson.GetBytes(data, "model").String() != expected.Model {
		return errors.New("oauth429 target model changed")
	}
	return nil
}

type oauth429Observer struct {
	repo OAuth429Repository
	wake chan struct{}
}

var oauth429HTTPObserver atomic.Pointer[oauth429Observer]

// ObserveOAuth429HTTP accepts only business requests carrying the server's
// correlation identity. Background tests never carry that identity. Retries
// retain it; the durable per-account/model window counts its first response.
func ObserveOAuth429HTTP(req *http.Request, accountID int64, status int) {
	observer := oauth429HTTPObserver.Load()
	if observer == nil || req == nil || req.GetBody == nil || status < 200 || status > 599 || req.Context().Value(qualityProbeContextKey{}) != nil {
		return
	}
	requestID, _ := req.Context().Value(ctxkey.ClientRequestID).(string)
	if requestID == "" {
		return
	}
	body, err := req.GetBody()
	if err != nil {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	_ = body.Close()
	if err != nil {
		return
	}
	model := gjson.GetBytes(raw, "model").String()
	if model == "" || len(model) > 256 {
		return
	}
	digest := sha256.Sum256([]byte(requestID))
	ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 500*time.Millisecond)
	defer cancel()
	queued, err := observer.repo.ObserveOAuth429(ctx, accountID, model, hex.EncodeToString(digest[:]), status)
	if err != nil {
		slog.Warn("oauth429 observation failed", "account_id", accountID)
		return
	}
	if queued {
		select {
		case observer.wake <- struct{}{}:
		default:
		}
	}
}

func (a *Account) IsOAuth429ModelBlocked(model string) bool {
	if a == nil {
		return false
	}
	entries, _ := a.Extra[OAuth429ModelBlocksKey].(map[string]any)
	return entries[model] != nil || entries[a.GetMappedModel(model)] != nil
}

// RunOAuth429Task runs exactly the two reserved calls. A crash after dispatch
// leaves an inconclusive slot, never permission to replay that billable call.
func (s *ScheduledTestRunnerService) RunOAuth429Task(ctx context.Context, repo OAuth429Repository, task *OAuth429Task) error {
	ctx, cancel := context.WithDeadline(ctx, task.ExpiresAt)
	defer cancel()
	for len(task.Runs) < 2 {
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
			return err
		}
		cfg := &PelicanTestConfig{QuestionKind: "candy", Prompt: CandyPrompt, ReasoningEffort: "high", ParallelCount: 1, ModelID: task.Model, Quality: &QualityPolicy{Action: QualityActionObserveOnly}}
		testCtx, stop := context.WithTimeout(context.WithValue(ctx, oauth429ExecutionKey{}, oauth429Execution{AccountID: task.AccountID, Model: task.Model}), 90*time.Second)
		// This direct executor addresses the original account; no group selection,
		// answer retry, scheduled plan, or qualityops switch is consulted.
		result, executeErr := s.runPelican(testCtx, task.AccountID, task.Model, cfg)
		run.Verdict = oauth429Verdict(result, executeErr, testCtx.Err(), task.Model)
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

var oauth429Integer = regexp.MustCompile(`^[0-9]{1,6}$`)

func oauth429Verdict(result *ScheduledTestResult, err, contextErr error, model string) string {
	if err != nil || contextErr != nil || result == nil || result.Status != "success" || result.ErrorMessage != "" || result.PelicanConfig == nil || result.PelicanConfig.ModelID != model {
		return "inconclusive"
	}
	answer := strings.TrimSpace(result.ResponseText)
	if !oauth429Integer.MatchString(answer) {
		return "inconclusive"
	}
	if strings.TrimLeft(answer, "0") == "21" {
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
			case <-s.oauth429Wake:
			}
		}
	}()
}
