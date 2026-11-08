package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/reauthruntime"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

const (
	AccountTokenGuardV2ProbePending   = "pending"
	AccountTokenGuardV2ProbeOK        = "ok"
	AccountTokenGuardV2ProbeAuth      = "auth"
	AccountTokenGuardV2ProbeTransient = "transient"
	accountTokenGuardV2PolicyBlocked  = "upstream request or policy denied (403); automatic re-login blocked"

	AccountTokenGuardV2ProbeInterval   = 30 * time.Minute
	AccountTokenGuardV2RetryInterval   = 5 * time.Minute
	accountTokenGuardV2LeaseDuration   = 2 * time.Minute
	accountTokenGuardV2CycleDuration   = 90 * time.Second
	accountTokenGuardV2ProbeWorkers    = 3
	AccountTokenGuardV2ReloginCooldown = 30 * time.Minute
	AccountTokenGuardV2FailThreshold   = 2

	accountTokenGuardV2RulesSettingKey = "account_token_guard_v2_rules"
)

type AccountTokenGuardV2Rules struct {
	ReloginBackoffMaxSeconds int `json:"relogin_backoff_max_seconds"`
	ReloginMaxFailures       int `json:"relogin_max_failures"`
	ProbeIntervalSeconds     int `json:"probe_interval_seconds"`
	RetryIntervalSeconds     int `json:"retry_interval_seconds"`
	ReloginCooldownSeconds   int `json:"relogin_cooldown_seconds"`
	FailStreakThreshold      int `json:"fail_streak_threshold"`
}

func defaultAccountTokenGuardV2Rules() AccountTokenGuardV2Rules {
	return AccountTokenGuardV2Rules{
		ReloginBackoffMaxSeconds: DefaultReloginBackoffMax,
		ReloginMaxFailures:       DefaultReloginMaxFailures,
		ProbeIntervalSeconds:     int(AccountTokenGuardV2ProbeInterval.Seconds()),
		RetryIntervalSeconds:     int(AccountTokenGuardV2RetryInterval.Seconds()),
		ReloginCooldownSeconds:   int(AccountTokenGuardV2ReloginCooldown.Seconds()),
		FailStreakThreshold:      AccountTokenGuardV2FailThreshold,
	}
}

func ValidateAccountTokenGuardV2Rules(rules AccountTokenGuardV2Rules) error {
	rules = NormalizeReloginBudgetRules(rules)
	if rules.ReloginMaxFailures < 1 || rules.ReloginMaxFailures > 20 {
		return errors.New("relogin_max_failures must be between 1 and 20")
	}
	if rules.ReloginBackoffMaxSeconds < rules.ReloginCooldownSeconds || rules.ReloginBackoffMaxSeconds > 604800 {
		return errors.New("relogin_backoff_max_seconds must be between relogin_cooldown_seconds and 604800")
	}
	if rules.ProbeIntervalSeconds < 60 || rules.ProbeIntervalSeconds > 86400 {
		return errors.New("probe_interval_seconds must be between 60 and 86400")
	}
	if rules.RetryIntervalSeconds < 30 || rules.RetryIntervalSeconds > 86400 {
		return errors.New("retry_interval_seconds must be between 30 and 86400")
	}
	if rules.ReloginCooldownSeconds < 60 || rules.ReloginCooldownSeconds > 604800 {
		return errors.New("relogin_cooldown_seconds must be between 60 and 604800")
	}
	if rules.FailStreakThreshold < 1 || rules.FailStreakThreshold > 10 {
		return errors.New("fail_streak_threshold must be between 1 and 10")
	}
	return nil
}

type AccountTokenGuardV2Record struct {
	RefreshFailureCode   string
	LastRefreshFailureAt *time.Time
	ReloginFailStreak    int
	LastCountedTaskID    int64
	AutoReloginExhausted bool
	AccountID            int64
	Enabled              bool
	AutoReloginEnabled   bool
	ProbeState           string
	ProbeDetail          string
	FailStreak           int
	LastProbeAt          *time.Time
	LastReauthAt         *time.Time
	NextProbeAt          time.Time
	CooldownUntil        *time.Time
	BlockedReason        string
	LeaseOwner           string
	LeaseUntil           *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type AccountTokenGuardV2ProbeCompletion struct {
	ProbeState    string
	ProbeDetail   string
	FailStreak    int
	LastProbeAt   time.Time
	LastReauthAt  *time.Time
	NextProbeAt   time.Time
	CooldownUntil *time.Time
	BlockedReason string
}

type AccountTokenGuardV2Repository interface {
	UpsertAccount(ctx context.Context, accountID int64, enabled, autoRelogin bool) error
	DeleteAccount(ctx context.Context, accountID int64) error
	PruneDeletedAccounts(ctx context.Context) (int64, error)
	GetAccount(ctx context.Context, accountID int64) (*AccountTokenGuardV2Record, error)
	ListAccounts(ctx context.Context) ([]AccountTokenGuardV2Record, error)
	ClaimDue(ctx context.Context, owner string, leaseDuration time.Duration, limit int, dueBefore time.Time) ([]AccountTokenGuardV2Record, error)
	ClaimAccount(ctx context.Context, accountID int64, owner string, leaseDuration time.Duration) (*AccountTokenGuardV2Record, error)
	ReleaseProbe(ctx context.Context, accountID int64, owner string) error
	CompleteProbe(ctx context.Context, accountID int64, owner string, result AccountTokenGuardV2ProbeCompletion) error
	MarkReauthRequested(ctx context.Context, accountID int64, requestedAt, cooldownUntil time.Time) error
	RescheduleEnabled(ctx context.Context) error
}

type AccountTokenGuardV2Settings interface {
	GetValue(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

type AccountTokenGuardV2AccountReader interface {
	GetAccount(ctx context.Context, id int64) (*Account, error)
	GetProxy(ctx context.Context, id int64) (*Proxy, error)
}

type AccountTokenGuardV2Prober interface {
	FetchOpenAIModelsListFresh(ctx context.Context, account *Account) (*OpenAIModelsResponse, error)
}

type AccountTokenGuardV2AccountInput struct {
	PreserveEnabled     bool
	PreserveAutoRelogin bool
	LoginEmail          string `json:"login_email"`
	CredentialMode      string `json:"credential_mode"`
	Engine              string `json:"engine"`
	ProxySource         string `json:"proxy_source"`
	ProxyID             *int64 `json:"proxy_id"`
	Password            string `json:"password"`
	TOTPSecret          string `json:"totp_secret"`
	OTPURL              string `json:"otp_url"`
	ClearPassword       bool   `json:"clear_password"`
	ClearTOTP           bool   `json:"clear_totp"`
	Enabled             bool   `json:"enabled"`
	AutoReloginEnabled  bool   `json:"auto_relogin_enabled"`
}

type AccountTokenGuardV2Account struct {
	RefreshFailure       *CredentialOperationFailure `json:"refresh_failure,omitempty"`
	ProbeFailure         *CredentialOperationFailure `json:"probe_failure,omitempty"`
	ReloginFailStreak    int                         `json:"relogin_fail_streak"`
	LastCountedTaskID    int64                       `json:"last_counted_task_id"`
	AutoReloginExhausted bool                        `json:"auto_relogin_exhausted"`
	AccountID            int64                       `json:"account_id"`
	AccountName          string                      `json:"account_name"`
	AccountStatus        string                      `json:"account_status"`
	Schedulable          bool                        `json:"schedulable"`
	Enabled              bool                        `json:"enabled"`
	AutoReloginEnabled   bool                        `json:"auto_relogin_enabled"`
	ProbeState           string                      `json:"probe_state"`
	ProbeDetail          string                      `json:"probe_detail"`
	FailStreak           int                         `json:"fail_streak"`
	LastProbeAt          *time.Time                  `json:"last_probe_at,omitempty"`
	LastReauthAt         *time.Time                  `json:"last_reauth_at,omitempty"`
	NextProbeAt          time.Time                   `json:"next_probe_at"`
	CooldownUntil        *time.Time                  `json:"cooldown_until,omitempty"`
	BlockedReason        string                      `json:"blocked_reason,omitempty"`
	LoginConfig          *OpenAIOAuthReauthConfig    `json:"login_config,omitempty"`
	LatestTask           *OpenAIOAuthReauthTask      `json:"latest_task,omitempty"`
}

type AccountTokenGuardV2Service struct {
	now      func() time.Time
	repo     AccountTokenGuardV2Repository
	settings AccountTokenGuardV2Settings
	accounts AccountTokenGuardV2AccountReader
	prober   AccountTokenGuardV2Prober
	reauth   *OpenAIOAuthReauthService

	leaseOwner string
	runMu      sync.Mutex
	startOnce  sync.Once
	rootCtx    context.Context
	cancel     context.CancelFunc
	doneCh     chan struct{}
}

func NewAccountTokenGuardV2Service(repo AccountTokenGuardV2Repository, settings AccountTokenGuardV2Settings, accounts AccountTokenGuardV2AccountReader, prober AccountTokenGuardV2Prober, reauth *OpenAIOAuthReauthService) *AccountTokenGuardV2Service {
	host, _ := os.Hostname()
	ctx, cancel := context.WithCancel(context.Background())
	return &AccountTokenGuardV2Service{
		now:  time.Now,
		repo: repo, settings: settings, accounts: accounts, prober: prober, reauth: reauth,
		leaseOwner: fmt.Sprintf("%s-%d-%s", host, os.Getpid(), uuid.NewString()),
		rootCtx:    ctx, cancel: cancel, doneCh: make(chan struct{}),
	}
}

func (s *AccountTokenGuardV2Service) GetRules(ctx context.Context) (AccountTokenGuardV2Rules, error) {
	rules := defaultAccountTokenGuardV2Rules()
	if s == nil || s.settings == nil {
		return rules, nil
	}
	raw, err := s.settings.GetValue(ctx, accountTokenGuardV2RulesSettingKey)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return rules, err
	}
	if strings.TrimSpace(raw) != "" {
		// Missing fields from saved old settings acquire safe, bounded defaults.
		rules.ReloginMaxFailures, rules.ReloginBackoffMaxSeconds = 0, 0
		if err := json.Unmarshal([]byte(raw), &rules); err != nil {
			return rules, err
		}
	}
	rules = NormalizeReloginBudgetRules(rules)
	if err := ValidateAccountTokenGuardV2Rules(rules); err != nil {
		return rules, err
	}
	return rules, nil
}

func (s *AccountTokenGuardV2Service) SaveRules(ctx context.Context, rules AccountTokenGuardV2Rules) (AccountTokenGuardV2Rules, error) {
	rules = NormalizeReloginBudgetRules(rules)
	if err := ValidateAccountTokenGuardV2Rules(rules); err != nil {
		return rules, infraerrors.BadRequest("TOKEN_GUARD_V2_RULES_INVALID", err.Error())
	}
	if s == nil || s.settings == nil {
		return rules, infraerrors.New(http.StatusInternalServerError, "TOKEN_GUARD_V2_RULES_SAVE_FAILED", "settings repository is unavailable")
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		return rules, err
	}
	if err := s.settings.Set(ctx, accountTokenGuardV2RulesSettingKey, string(raw)); err != nil {
		return rules, infraerrors.New(http.StatusInternalServerError, "TOKEN_GUARD_V2_RULES_SAVE_FAILED", "failed to save inspection rules")
	}
	if err := s.repo.RescheduleEnabled(ctx); err != nil {
		return rules, infraerrors.New(http.StatusInternalServerError, "TOKEN_GUARD_V2_RULES_RESCHEDULE_FAILED", "rules were saved but enabled accounts could not be rescheduled")
	}
	return rules, nil
}

func (s *AccountTokenGuardV2Service) Start() {
	if s == nil || s.repo == nil || s.accounts == nil || s.prober == nil || s.reauth == nil {
		return
	}
	s.startOnce.Do(func() {
		go func() {
			defer close(s.doneCh)
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			runCycle := func() {
				ctx, cancel := context.WithTimeout(s.rootCtx, accountTokenGuardV2CycleDuration)
				defer cancel()
				if rows, err := s.repo.ListAccounts(ctx); err == nil && len(rows) > 0 {
					s.reauth.EnsureWorker()
				}
				if err := s.reauth.QueueMissingExcelAuthorizations(ctx); err != nil {
					slog.Warn("openai_excel_authorization_cycle_failed", "error", err)
				}
				if _, err := s.RunDue(ctx); err != nil {
					slog.Warn("account_token_guard_v2_cycle_failed", "error", err)
				}
			}
			if s.rootCtx.Err() != nil {
				return
			}
			runCycle()
			for {
				select {
				case <-s.rootCtx.Done():
					return
				case <-ticker.C:
					runCycle()
				}
			}
		}()
	})
}

func (s *AccountTokenGuardV2Service) Stop() {
	if s == nil {
		return
	}
	s.reauth.stopWorker()
	s.cancel()
	s.startOnce.Do(func() { close(s.doneCh) })
	select {
	case <-s.doneCh:
	case <-time.After(10 * time.Second):
	}
}

func (s *AccountTokenGuardV2Service) SaveAccount(ctx context.Context, accountID int64, input AccountTokenGuardV2AccountInput) (*AccountTokenGuardV2Account, error) {
	if accountID <= 0 {
		return nil, infraerrors.BadRequest("TOKEN_GUARD_V2_ACCOUNT_INVALID", "invalid account id")
	}
	preserveSwitches := false
	if input.PreserveEnabled || input.PreserveAutoRelogin {
		existing, err := s.repo.GetAccount(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			preserveSwitches = input.PreserveEnabled && input.PreserveAutoRelogin
			if input.PreserveEnabled {
				input.Enabled = existing.Enabled
			}
			if input.PreserveAutoRelogin {
				input.AutoReloginEnabled = existing.AutoReloginEnabled
			}
		}
	}
	if _, err := s.reauth.SaveCredentialConfig(ctx, accountID, OpenAIOAuthReauthConfigInput{
		LoginEmail: input.LoginEmail, CredentialMode: input.CredentialMode, Engine: input.Engine,
		ProxySource: input.ProxySource, ProxyID: input.ProxyID,
		Password: input.Password, TOTPSecret: input.TOTPSecret, OTPURL: input.OTPURL,
		ClearPassword: input.ClearPassword, ClearTOTP: input.ClearTOTP,
	}); err != nil {
		return nil, err
	}
	if !preserveSwitches {
		if err := s.repo.UpsertAccount(ctx, accountID, input.Enabled, input.AutoReloginEnabled); err != nil {
			return nil, infraerrors.New(http.StatusInternalServerError, "TOKEN_GUARD_V2_SAVE_FAILED", "failed to save monitored account")
		}
	}
	return s.accountView(ctx, accountID)
}

func (s *AccountTokenGuardV2Service) RemoveAccount(ctx context.Context, accountID int64) error {
	if err := s.repo.DeleteAccount(ctx, accountID); err != nil {
		return infraerrors.New(http.StatusInternalServerError, "TOKEN_GUARD_V2_DELETE_FAILED", "failed to remove monitored account")
	}
	return nil
}

func (s *AccountTokenGuardV2Service) ListAccounts(ctx context.Context) ([]AccountTokenGuardV2Account, error) {
	records, err := s.repo.ListAccounts(ctx)
	if err != nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "TOKEN_GUARD_V2_LIST_FAILED", "failed to list monitored accounts")
	}
	result := make([]AccountTokenGuardV2Account, 0, len(records))
	for _, record := range records {
		view, viewErr := s.recordView(ctx, record)
		if viewErr != nil {
			view = &AccountTokenGuardV2Account{
				RefreshFailure:    storedRefreshFailure(record),
				ReloginFailStreak: record.ReloginFailStreak, LastCountedTaskID: record.LastCountedTaskID, AutoReloginExhausted: record.AutoReloginExhausted,
				AccountID: record.AccountID, Enabled: record.Enabled, AutoReloginEnabled: record.AutoReloginEnabled,
				ProbeState: record.ProbeState, ProbeDetail: safeProbeOperationDetail(record.ProbeState, record.ProbeDetail), ProbeFailure: probeOperationFailure(record.ProbeState, record.ProbeDetail, record.LastProbeAt), FailStreak: record.FailStreak,
				LastProbeAt: record.LastProbeAt, LastReauthAt: record.LastReauthAt, NextProbeAt: record.NextProbeAt,
				CooldownUntil: record.CooldownUntil, BlockedReason: "account or login configuration is unavailable",
			}
		}
		result = append(result, *view)
	}
	return result, nil
}

func (s *AccountTokenGuardV2Service) accountView(ctx context.Context, accountID int64) (*AccountTokenGuardV2Account, error) {
	record, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "TOKEN_GUARD_V2_READ_FAILED", "failed to read monitored account")
	}
	if record == nil {
		return nil, infraerrors.New(http.StatusNotFound, "TOKEN_GUARD_V2_NOT_FOUND", "monitored account not found")
	}
	return s.recordView(ctx, *record)
}

func (s *AccountTokenGuardV2Service) recordView(ctx context.Context, record AccountTokenGuardV2Record) (*AccountTokenGuardV2Account, error) {
	account, err := s.accounts.GetAccount(ctx, record.AccountID)
	if err != nil || account == nil {
		return nil, errors.New("account not found")
	}
	reauthStatus, err := s.reauth.GetStatus(ctx, record.AccountID)
	if err != nil {
		return nil, err
	}
	lastReauthAt := record.LastReauthAt
	if reauthStatus.Task != nil && reauthStatus.Task.Status == OpenAIOAuthReauthStatusSucceeded && reauthStatus.Task.FinishedAt != nil {
		lastReauthAt = reauthStatus.Task.FinishedAt
	}
	return &AccountTokenGuardV2Account{
		RefreshFailure:    storedRefreshFailure(record),
		ReloginFailStreak: record.ReloginFailStreak, LastCountedTaskID: record.LastCountedTaskID, AutoReloginExhausted: record.AutoReloginExhausted,
		AccountID: record.AccountID, AccountName: account.Name, AccountStatus: account.Status, Schedulable: account.Schedulable,
		Enabled: record.Enabled, AutoReloginEnabled: record.AutoReloginEnabled,
		ProbeState: record.ProbeState, ProbeDetail: safeProbeOperationDetail(record.ProbeState, record.ProbeDetail), ProbeFailure: probeOperationFailure(record.ProbeState, record.ProbeDetail, record.LastProbeAt), FailStreak: record.FailStreak,
		LastProbeAt: record.LastProbeAt, LastReauthAt: lastReauthAt, NextProbeAt: record.NextProbeAt,
		CooldownUntil: record.CooldownUntil, BlockedReason: record.BlockedReason,
		LoginConfig: reauthStatus.Config, LatestTask: reauthStatus.Task,
	}, nil
}

func (s *AccountTokenGuardV2Service) RunDue(ctx context.Context) (int, error) {
	// A cycle waits for its probes and final lease writes before releasing this
	// lock. Overlapping callers must not multiply the bounded worker pool.
	if !s.runMu.TryLock() {
		return 0, nil
	}
	defer s.runMu.Unlock()
	startedAt := time.Now()
	ctx, cancel := context.WithTimeout(ctx, accountTokenGuardV2CycleDuration)
	defer cancel()
	// Freeze eligibility at cycle start. A long cycle must not repeatedly probe
	// accounts whose retry interval elapses while other accounts are running.
	dueBefore := s.clockNow()
	// Clean all orphaned records, including paused accounts and future probes.
	// Only the database's account deletion state is authoritative, not read errors.
	removed, err := s.repo.PruneDeletedAccounts(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune deleted credential operations accounts: %w", err)
	}
	if removed > 0 {
		slog.Info("account_token_guard_v2_deleted_accounts_pruned", "count", removed)
	}
	group, workCtx := errgroup.WithContext(ctx)
	var completed atomic.Int64
	for range accountTokenGuardV2ProbeWorkers {
		group.Go(func() error {
			for {
				if err := workCtx.Err(); err != nil {
					return err
				}
				// Claim only when this slot is free: no leased account waits
				// behind a slow probe, and every claim has its own fencing token.
				owner := s.newProbeOwner()
				records, err := s.repo.ClaimDue(workCtx, owner, accountTokenGuardV2LeaseDuration, 1, dueBefore)
				if err != nil {
					return err
				}
				if len(records) == 0 {
					return nil
				}
				if err := s.runProbe(workCtx, records[0]); err != nil {
					return err
				}
				completed.Add(1)
			}
		})
	}
	err = group.Wait()
	slog.Info("account_token_guard_v2_cycle_completed", "processed", completed.Load(), "duration_ms", time.Since(startedAt).Milliseconds(), "cancelled", ctx.Err() != nil)
	return int(completed.Load()), err
}

func (s *AccountTokenGuardV2Service) newProbeOwner() string {
	return s.leaseOwner + "/" + uuid.NewString()
}

func (s *AccountTokenGuardV2Service) ProbeNow(ctx context.Context, accountID int64) (*AccountTokenGuardV2Account, error) {
	existing, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "TOKEN_GUARD_V2_READ_FAILED", "failed to read monitored account")
	}
	if existing == nil {
		return nil, infraerrors.New(http.StatusNotFound, "TOKEN_GUARD_V2_NOT_FOUND", "monitored account not found")
	}
	record, err := s.repo.ClaimAccount(ctx, accountID, s.newProbeOwner(), accountTokenGuardV2LeaseDuration)
	if err != nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "TOKEN_GUARD_V2_CLAIM_FAILED", "failed to start account inspection")
	}
	if record == nil {
		return nil, infraerrors.Conflict("TOKEN_GUARD_V2_PROBE_BUSY", "account inspection is already running")
	}
	if err := s.runProbe(ctx, *record); err != nil {
		return nil, err
	}
	return s.accountView(ctx, accountID)
}

func (s *AccountTokenGuardV2Service) ReloginNow(ctx context.Context, accountID int64) (*OpenAIOAuthReauthTask, error) {
	if record, err := s.repo.GetAccount(ctx, accountID); err != nil || record == nil {
		return nil, infraerrors.New(http.StatusNotFound, "TOKEN_GUARD_V2_NOT_FOUND", "monitored account not found")
	}
	rules, err := s.GetRules(ctx)
	if err != nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "TOKEN_GUARD_V2_RULES_READ_FAILED", "failed to read inspection rules")
	}
	task, err := s.reauth.CreateTask(ctx, accountID)
	if err != nil {
		return nil, err
	}
	now := s.clockNow()
	_ = s.repo.MarkReauthRequested(ctx, accountID, now, now.Add(time.Duration(rules.ReloginCooldownSeconds)*time.Second))
	return task, nil
}

func (s *AccountTokenGuardV2Service) runProbe(ctx context.Context, record AccountTokenGuardV2Record) error {
	owner := record.LeaseOwner
	// CompleteProbe normally clears the lease. Early failures and cancellation
	// still release this claim without changing the probe history or due time.
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := s.repo.ReleaseProbe(releaseCtx, record.AccountID, owner); err != nil {
			slog.Warn("account_token_guard_v2_lease_release_failed", "account_id", record.AccountID)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	rules, err := s.GetRules(ctx)
	if err != nil {
		slog.Warn("account_token_guard_v2_rules_read_failed", "error", err)
		return err
	}
	now := s.clockNow()
	completion := AccountTokenGuardV2ProbeCompletion{
		ProbeState: AccountTokenGuardV2ProbeTransient, ProbeDetail: "inspection failed",
		FailStreak: record.FailStreak, LastProbeAt: now, NextProbeAt: now.Add(time.Duration(rules.ProbeIntervalSeconds) * time.Second),
		CooldownUntil: record.CooldownUntil,
	}
	account, err := s.accounts.GetAccount(ctx, record.AccountID)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth || account.IsCredentialShadow() || account.IsOpenAIPersonalAccessToken() || account.IsOpenAIAgentIdentity() {
		completion.ProbeDetail = "account is no longer an eligible OpenAI OAuth parent account"
		completion.BlockedReason = completion.ProbeDetail
		return s.completeProbe(ctx, record.AccountID, owner, completion)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	state, detail := s.probeAccount(probeCtx, account)
	cancel()
	if err := ctx.Err(); err != nil {
		// Stopping the scheduler is not an account authentication failure.
		return err
	}
	completion.ProbeState = state
	completion.ProbeDetail = detail
	completion.BlockedReason = ""
	budgetRepo, hasBudget := s.repo.(AccountReloginBudgetRepository)
	pending := false
	if hasBudget {
		var updated *AccountTokenGuardV2Record
		updated, pending, err = budgetRepo.ReconcileReloginBudget(ctx, record.AccountID, owner, rules, now, state)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || updated == nil {
			completion.BlockedReason = "re-login budget could not be reconciled"
			return s.completeProbe(ctx, record.AccountID, owner, completion)
		}
		record = *updated
		completion.CooldownUntil = record.CooldownUntil
	} else {
		completion.BlockedReason = "automatic re-login budget is unavailable"
	}
	if record.AutoReloginExhausted {
		completion.BlockedReason = "automatic re-login budget exhausted; explicit recovery required"
	} else if pending {
		completion.BlockedReason = "re-login task active or history reconciliation pending"
	}
	if state == AccountTokenGuardV2ProbeTransient && detail == accountTokenGuardV2PolicyBlocked {
		completion.BlockedReason = detail
	}
	switch state {
	case AccountTokenGuardV2ProbeOK:
		completion.FailStreak = 0
		// The reconciler alone can clear a budget cooldown after a verified
		// success. A plain OK must retain an explicit recovery's cooldown.
	case AccountTokenGuardV2ProbeAuth:
		completion.FailStreak++
		completion.NextProbeAt = now.Add(time.Duration(rules.RetryIntervalSeconds) * time.Second)
		cooldownActive := record.CooldownUntil != nil && record.CooldownUntil.After(now)
		if cooldownActive && completion.BlockedReason == "" {
			completion.BlockedReason = "automatic re-login cooling down"
		}
		if hasBudget && record.Enabled && record.AutoReloginEnabled && !record.AutoReloginExhausted && !pending && completion.FailStreak >= rules.FailStreakThreshold && !cooldownActive {
			task, taskErr := s.reauth.createTaskForProfile(ctx, record.AccountID, "codex", func(createCtx context.Context, id int64, hash string) (*OpenAIOAuthReauthTaskRecord, error) {
				return budgetRepo.CreateAutomaticRelogin(createCtx, id, hash, owner, rules, s.clockNow())
			})
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if taskErr == nil && task != nil {
				completion.LastReauthAt = &now
				cooldown := now.Add(time.Duration(rules.ReloginCooldownSeconds) * time.Second)
				completion.CooldownUntil = &cooldown
				completion.ProbeDetail = detail + "; automatic re-login queued"
			} else if infraerrors.Code(taskErr) == http.StatusConflict {
				completion.ProbeDetail = detail + "; re-login task already active or budget gated"
			} else if taskErr != nil {
				completion.BlockedReason = "automatic re-login could not be queued"
				completion.ProbeDetail = detail + "; " + completion.BlockedReason
				slog.Warn("account_token_guard_v2_relogin_queue_failed", "account_id", record.AccountID, "reason", safeTokenGuardReloginReason(taskErr))
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return s.completeProbe(ctx, record.AccountID, owner, completion)
}

func safeTokenGuardReloginReason(err error) string {
	switch reason := infraerrors.Reason(err); reason {
	case "OPENAI_REAUTH_UNAVAILABLE", "OPENAI_REAUTH_WORKER_UNAVAILABLE", "OPENAI_REAUTH_CONFIG_REQUIRED", "OPENAI_REAUTH_CONFIG_READ_FAILED", "OPENAI_REAUTH_CONFIG_DECRYPT_FAILED", "OPENAI_REAUTH_ENCRYPTION_KEY_REQUIRED", "OPENAI_REAUTH_CREDENTIAL_SNAPSHOT_FAILED", "OPENAI_REAUTH_ACCOUNT_NOT_FOUND", "OPENAI_REAUTH_ACCOUNT_INVALID", "OPENAI_REAUTH_TASK_CREATE_FAILED":
		return reason
	default:
		return "unknown"
	}
}

func (s *AccountTokenGuardV2Service) completeProbe(ctx context.Context, accountID int64, owner string, completion AccountTokenGuardV2ProbeCompletion) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.repo.CompleteProbe(persistCtx, accountID, owner, completion); err != nil {
		slog.Warn("account_token_guard_v2_probe_save_failed", "account_id", accountID, "error", err)
		return err
	}
	return nil
}

func (s *AccountTokenGuardV2Service) probeAccount(ctx context.Context, account *Account) (string, string) {
	if strings.TrimSpace(account.GetCredential("access_token")) == "" {
		return AccountTokenGuardV2ProbeAuth, "account has no access token"
	}
	_, err := s.prober.FetchOpenAIModelsListFresh(ctx, account)
	if err == nil {
		return AccountTokenGuardV2ProbeOK, "credential accepted"
	}
	var upstreamErr *codexModelsManifestUpstreamError
	if errors.As(err, &upstreamErr) && upstreamErr.statusCode == http.StatusForbidden {
		policy, _, _ := detectOpenAICyberPolicy(upstreamErr.body)
		if policy || isOpenAIUpstreamSessionBlocked(upstreamErr.body) || isOpenAIRequestScoped403(account, upstreamErr.statusCode, upstreamErr.body) {
			// These denials say nothing about credential health. Never issue a
			// fresh login/session to bypass them, or echo the upstream body.
			return AccountTokenGuardV2ProbeTransient, accountTokenGuardV2PolicyBlocked
		}
	}
	var code string
	if errors.As(err, &upstreamErr) {
		code = credentialOperationEnvelope(upstreamErr.body)
		if code == "unknown" {
			switch {
			case upstreamErr.statusCode == http.StatusUnauthorized:
				code = "requires_relogin"
			case upstreamErr.statusCode == http.StatusForbidden:
				code = "authorization_denied"
			case upstreamErr.statusCode == http.StatusTooManyRequests:
				code = "rate_limited"
			case upstreamErr.statusCode >= 500:
				code = "service_error"
			}
		}
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "timeout"
	} else {
		var networkError interface{ Timeout() bool }
		if errors.As(err, &networkError) {
			code = "transport_error"
			if networkError.Timeout() {
				code = "timeout"
			}
		} else {
			code = credentialOperationText(err.Error())
		}
	}
	if code == "requires_relogin" {
		return AccountTokenGuardV2ProbeAuth, credentialOperationDetail(code)
	}
	// Verification, retirement, security denials and unknown failures cannot
	// create a new login session. Existing retry budgets and leases still apply.
	return AccountTokenGuardV2ProbeTransient, credentialOperationDetail(code)
}

// WorkerStatus separates saved login credentials from execution availability.
func (s *AccountTokenGuardV2Service) WorkerStatus() reauthruntime.Status {
	return s.reauth.WorkerStatus()
}
