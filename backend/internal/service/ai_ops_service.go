package service

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

// This host switch is independent of model prompts and persisted policy. An
// upgrade never opts a deployment into production account modifications.
func (s *OpsService) AIOpsControlEnabled() bool {
	return s != nil && os.Getenv("SUB2API_AI_OPS_CONTROL_ENABLED") == "true" &&
		(s.cfg == nil || (s.cfg.Runtime.Role != config.RuntimeRoleGateway && s.cfg.Runtime.RegionalSchedulerFile == ""))
}

func (s *OpsService) AIOpsRepository() (AIOpsRepository, error) {
	if s == nil || s.accountRepo == nil {
		return nil, ErrAIOpsUnavailable
	}
	repo, ok := s.accountRepo.(AIOpsRepository)
	if !ok {
		return nil, ErrAIOpsUnavailable
	}
	return repo, nil
}

// The application, not the SDK, owns reconciliation and policy-authorized
// actions. The repository serializes globally across full instances and fences
// each write against current policy, capacity, account and action ownership.
func (s *OpsService) StartAIOpsController(ctx context.Context) {
	if !s.AIOpsControlEnabled() {
		return
	}
	repo, err := s.AIOpsRepository()
	if err != nil {
		return
	}
	s.aiOpsMu.Lock()
	defer s.aiOpsMu.Unlock()
	if s.aiOpsCancel != nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.aiOpsCancel = cancel
	s.aiOpsContext = workerCtx
	s.aiOpsStopping = false
	done := make(chan struct{})
	s.aiOpsDone = done
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
			if !s.AIOpsControlEnabled() {
				continue
			}
			cycleCtx, cycleCancel := context.WithTimeout(workerCtx, 20*time.Second)
			// Reconcile even when halted: halting new writes must not discard
			// unknown outcomes, propagation checks or preauthorized compensation.
			if err := repo.ReconcileAIOps(cycleCtx); err != nil {
				slog.Warn("AI operations reconciliation incomplete")
			} else if err := repo.AIOpsAutoTick(cycleCtx); err != nil {
				slog.Warn("AI operations automatic cycle deferred")
			}
			cycleCancel()
		}
	}()
}

func (s *OpsService) StopAIOpsController() {
	if s == nil {
		return
	}
	s.aiOpsMu.Lock()
	s.aiOpsStopping = true
	cancel, done := s.aiOpsCancel, s.aiOpsDone
	s.aiOpsMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	s.aiOpsProbeWG.Wait()
	s.aiOpsMu.Lock()
	if s.aiOpsDone == done {
		s.aiOpsCancel, s.aiOpsDone = nil, nil
	}
	s.aiOpsMu.Unlock()
}

func (s *OpsService) SetAIOpsProbeRunner(tests *AccountTestService) {
	if s == nil || tests == nil {
		return
	}
	s.aiOpsMu.Lock()
	defer s.aiOpsMu.Unlock()
	s.aiOpsProbeRunner = func(ctx context.Context, id int64, model, effort string) (bool, error) {
		ctx, stop := context.WithCancel(ctx)
		defer stop()
		account, err := tests.accountRepo.GetByID(ctx, id)
		if err != nil || account == nil || !account.IsOpenAIOAuth() || account.IsShadow() || account.IsExcelBPSEnabled() || account.IsSIWCEnabled() || account.IsExcelOAuth() || account.HasManagedOpenAIConnections() {
			return false, ErrAIOpsBlocked
		}
		ctx = context.WithValue(ctx, aiOpsRecoveryProbeKey{}, true)
		w := &pelicanRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: stop}
		c, _ := gin.CreateTestContext(w)
		c.Request = (&http.Request{Header: make(http.Header)}).WithContext(ctx)
		err = tests.TestPelicanAccountConnection(c, id, model, "Reply with exactly OK. Do not use tools or add any other text.", effort)
		answer, message := parsePelicanOutput(w.Body.String())
		if w.overflow || message != "" {
			return false, ErrAIOpsBlocked
		}
		return err == nil && strings.TrimSpace(answer) == "OK", err
	}
}

type aiOpsRecoveryProbeKey struct{}

// A probe is explicitly authorized and persistently reserved before the paid
// request. A lost response or process crash does not replay that request.
func (s *OpsService) StartAIOpsProbe(ctx context.Context, accountID int64, model string, actorID int64) (AIOpsProbe, error) {
	if !s.AIOpsControlEnabled() {
		return AIOpsProbe{}, ErrAIOpsUnavailable
	}
	repo, err := s.AIOpsRepository()
	if err != nil {
		return AIOpsProbe{}, err
	}
	s.aiOpsMu.Lock()
	defer s.aiOpsMu.Unlock()
	if s.aiOpsStopping || s.aiOpsContext == nil || s.aiOpsProbeRunner == nil {
		return AIOpsProbe{}, ErrAIOpsUnavailable
	}
	probe, err := repo.ReserveAIOpsProbe(ctx, accountID, model, actorID)
	if err != nil {
		return probe, err
	}
	run, parent := s.aiOpsProbeRunner, s.aiOpsContext
	s.aiOpsProbeWG.Add(1)
	go func() {
		defer s.aiOpsProbeWG.Done()
		probeCtx, cancel := context.WithTimeout(parent, time.Minute)
		defer cancel()
		passed, err := run(probeCtx, accountID, probe.Model, probe.Effort)
		if err != nil || probeCtx.Err() != nil {
			passed = false
		}
		finishCtx, finishCancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer finishCancel()
		if _, err := repo.CompleteAIOpsProbe(finishCtx, probe.ID, passed, actorID); err != nil {
			slog.Warn("AI operations recovery probe outcome unresolved")
		}
	}()
	return probe, nil
}
