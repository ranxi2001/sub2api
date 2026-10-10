package service

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const accountOpsSettingsKey = "account_ops_notifications_v1"

type AccountOpsConfig struct {
	Enabled             bool                    `json:"enabled"`
	Recipient           string                  `json:"recipient"`
	EmailName           string                  `json:"email_name,omitempty"`
	BalanceLow          bool                    `json:"balance_low"`
	WeeklyQuota         bool                    `json:"weekly_quota"`
	CooldownMinutes     int                     `json:"cooldown_minutes"`
	Webhooks            []AccountOpsWebhook     `json:"webhooks"`
	Groups              []AccountOpsGroup       `json:"groups"`
	GroupsInitialized   bool                    `json:"groups_initialized,omitempty"`
	UngroupedAccountIDs []int64                 `json:"ungrouped_account_ids,omitempty"`
	BalanceThresholds   []AccountOpsBalanceRule `json:"balance_thresholds"`
	QuotaThresholds     []AccountOpsQuotaRule   `json:"quota_thresholds"`
}

func defaultAccountOpsConfig() AccountOpsConfig {
	return AccountOpsConfig{BalanceLow: true, WeeklyQuota: true, CooldownMinutes: 60}
}
func ValidateAccountOpsConfig(c AccountOpsConfig) (err error) {
	defer func() {
		if err != nil && !errors.Is(err, ErrAccountOpsConfigValidation) {
			err = accountOpsConfigValidation(err.Error())
		}
	}()
	if c.CooldownMinutes < 5 || c.CooldownMinutes > 1440 {
		return errors.New("cooldown_minutes must be between 5 and 1440")
	}
	if c.Recipient != "" {
		address, err := mail.ParseAddress(c.Recipient)
		if err != nil || address.Address != c.Recipient || strings.ContainsAny(c.Recipient, "\r\n") || len(c.Recipient) > 254 {
			return errors.New("recipient must be one email address")
		}
	}
	if err := validateAccountOpsExtras(c); err != nil {
		return err
	}
	if c.Enabled && (!c.hasDestination() || (!c.BalanceLow && !c.WeeklyQuota && !c.hasThreshold() && !c.hasQuotaThreshold())) {
		return errors.New("select an alert type and configure a recipient before enabling")
	}
	return nil
}
func (c AccountOpsConfig) Allows(kind string) bool {
	return c.Enabled && c.hasDestination() && ((kind == "balance_low" && c.BalanceLow) || (kind == "weekly_quota" && c.WeeklyQuota) || (kind == "balance_threshold" && c.hasThreshold()) || (kind == "quota_threshold" && c.hasQuotaThreshold()))
}

type AccountOpsEvent struct {
	ID                  string                        `json:"id,omitempty"`
	EpisodeID           string                        `json:"episode_id,omitempty"`
	Phase               string                        `json:"phase,omitempty"`
	NotificationEnabled *bool                         `json:"notification_enabled,omitempty"`
	Criteria            string                        `json:"-"`
	AccountID           int64                         `json:"account_id"`
	AccountName         string                        `json:"account_name"`
	GroupID             string                        `json:"group_id,omitempty"`
	GroupName           string                        `json:"group_name,omitempty"`
	Kind                string                        `json:"kind"`
	Signal              string                        `json:"signal"`
	HTTPStatus          int                           `json:"http_status"`
	FirstSeen           time.Time                     `json:"first_seen"`
	LastSeen            time.Time                     `json:"last_seen"`
	Occurrences         int64                         `json:"occurrences"`
	State               string                        `json:"state"`
	LastSentAt          *time.Time                    `json:"last_sent_at"`
	NextSendAt          time.Time                     `json:"next_send_at"`
	Attempts            int                           `json:"attempts"`
	Lease               string                        `json:"-"`
	Details             *AccountOpsDetails            `json:"details,omitempty"`
	Deliveries          map[string]AccountOpsDelivery `json:"deliveries,omitempty"`
	Identity            string                        `json:"-"`
}
type AccountOpsRepository interface {
	Record(context.Context, AccountOpsEvent) error
	Claim(context.Context) (*AccountOpsEvent, error)
	Complete(context.Context, *AccountOpsEvent, string, time.Duration) error
	SuppressDisabled(context.Context, AccountOpsConfig) error
	List(context.Context, int, int) ([]AccountOpsEvent, error)
}
type accountOpsEmailSender interface {
	SendEmail(context.Context, string, string, string) error
}

type AccountOpsService struct {
	thresholdLookupTimeout time.Duration
	usageLogs              UsageLogRepository
	geminiQuota            *GeminiQuotaService
	newAPIGroupReader      interface {
		GetBinding(context.Context, int64) (*NewAPIAccountBinding, error)
	}
	newAPIGroupCache sync.Map
	accounts         AccountRepository
	usageCache       *UsageCache
	encryptor        SecretEncryptor
	fixedKey         bool
	timezone         *time.Location
	robotClient      *http.Client
	robotMu          sync.Mutex
	robotLast        map[string]time.Time
	autoSeen         map[int64]bool
	autoConfigMu     sync.Mutex
	autoGroups       GroupRepository
	autoAccounts     AccountConcurrencyRepository
	autoConfig       atomic.Value
	autoBlocked      atomic.Bool
	autoResults      chan AccountConcurrencyResult
	settings         SettingRepository
	repo             AccountOpsRepository
	email            accountOpsEmailSender
	config           atomic.Value
	queue            chan AccountOpsEvent
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	lifecycle        sync.Mutex
	settingsMu       sync.Mutex
	dropped          atomic.Uint64
	failures         atomic.Uint64
}

func NewAccountOpsService(settings SettingRepository, repo AccountOpsRepository, email accountOpsEmailSender) *AccountOpsService {
	s := &AccountOpsService{settings: settings, repo: repo, email: email, queue: make(chan AccountOpsEvent, 256), timezone: time.FixedZone("Asia/Shanghai", 8*3600), robotLast: make(map[string]time.Time)}
	s.config.Store(defaultAccountOpsConfig())
	s.autoConfig.Store(DefaultOAuthAutoConfig())
	s.autoResults = make(chan AccountConcurrencyResult, 1024)
	s.autoSeen = make(map[int64]bool)
	return s
}
func (s *AccountOpsService) GetConfig(ctx context.Context) (AccountOpsConfig, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	c, err := s.loadConfig(ctx)
	if err == nil {
		s.config.Store(c)
	}
	return c.public(), err
}
func (s *AccountOpsService) SaveConfig(ctx context.Context, c AccountOpsConfig) error {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	result := &opsConfigResult{}
	ctx = context.WithValue(ctx, opsConfigResultKey{}, result)
	update := func(lockedCtx context.Context) error { return s.saveConfig(lockedCtx, c) }
	var err error
	if lock, ok := s.repo.(accountOpsConfigLocker); ok {
		err = lock.WithAccountOpsConfigLock(ctx, update)
	} else {
		err = update(ctx)
	}
	if err == nil && result.set {
		s.config.Store(result.cfg)
	}
	return err
}

func (s *AccountOpsService) Observe(account *Account, status int, headers http.Header, body []byte) {
	if s == nil || account == nil || account.ID <= 0 {
		return
	}
	c := s.currentConfig()
	if !c.Enabled {
		return
	}
	kind, signal := classifyAccountOpsFailure(account.Platform, status, headers, body)
	if !c.Allows(kind) {
		return
	}
	name := []rune(account.Name)
	if len(name) > 120 {
		name = name[:120]
	}
	group := s.cachedAccountOpsGroupAssignment(account, c)
	event := AccountOpsEvent{AccountID: account.ID, AccountName: string(name), GroupID: group.ID, GroupName: group.Name, Kind: kind, Signal: signal, HTTPStatus: status}
	// The hot request path never waits for SMTP or the database. Raw errors and credentials do not enter the queue.
	select {
	case s.queue <- event:
	default:
		s.dropped.Add(1)
	}
}
func (s *AccountOpsService) Start() {
	s.start(true)
}

// Request observations and auto-configuration must drain on every replica.
// Only the primary delivers the shared notification queue.
func (s *AccountOpsService) start(deliverNotifications bool) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(2)
	go s.runAutoConfig(ctx)
	go func() {
		defer s.wg.Done()
		s.refreshConfig(ctx)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-s.queue:
				if !s.currentConfig().Allows(event.Kind) {
					continue
				}
				query, stop := context.WithTimeout(ctx, 3*time.Second)
				err := s.repo.Record(query, event)
				stop()
				if err != nil {
					s.failures.Add(1)
				}
			case <-ticker.C:
				if s.refreshConfig(ctx) && deliverNotifications {
					s.scanBalances(ctx)
					s.deliver(ctx)
				}
			}
		}
	}()
}
func (s *AccountOpsService) Stop() {
	s.lifecycle.Lock()
	cancel := s.cancel
	s.lifecycle.Unlock()
	if cancel != nil {
		cancel()
		s.wg.Wait()
	}
}
func (s *AccountOpsService) refreshConfig(ctx context.Context) bool {
	query, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := s.GetConfig(query)
	if err != nil {
		s.config.Store(defaultAccountOpsConfig())
		s.failures.Add(1)
		return false
	}
	if s.accounts != nil {
		if reconciled, reconcileErr := s.ensureManualAccountOpsGroups(query); reconcileErr != nil {
			s.failures.Add(1)
			return false
		} else {
			c = reconciled
		}
	}
	if err = s.repo.SuppressDisabled(query, c); err != nil {
		s.failures.Add(1)
		return false
	}
	return c.Enabled
}
func (s *AccountOpsService) deliver(ctx context.Context) {
	for i := 0; i < 10 && ctx.Err() == nil; i++ {
		query, cancel := context.WithTimeout(ctx, 5*time.Second)
		event, err := s.repo.Claim(query)
		cancel()
		if err != nil {
			s.failures.Add(1)
			return
		}
		if event == nil {
			return
		}
		s.deliverEvent(ctx, event)
	}
}
func (s *AccountOpsService) deliverEvent(ctx context.Context, event *AccountOpsEvent) {
	s.deliverNotification(ctx, event)
}

func (s *AccountOpsService) List(ctx context.Context, offset, limit int) ([]AccountOpsEvent, error) {
	return s.repo.List(ctx, offset, limit)
}
func (s *AccountOpsService) RuntimeCounters() (uint64, uint64) {
	return s.dropped.Load(), s.failures.Load()
}

func (s *AccountOpsService) currentConfig() AccountOpsConfig {
	c, _ := s.config.Load().(AccountOpsConfig)
	return c
}

type accountOpsObservedKey struct{}

func (s *RateLimitService) observeAccountOps(ctx context.Context, account *Account, status int, headers http.Header, body []byte) context.Context {
	if s == nil || s.accountOps == nil || ctx.Value(accountOpsObservedKey{}) != nil {
		return ctx
	}
	s.accountOps.Observe(account, status, headers, body)
	return context.WithValue(ctx, accountOpsObservedKey{}, true)
}

func accountOpsSignalLabel(signal string) string {
	labels := map[string]string{"balance_error_code": "明确的余额错误码", "balance_error_message": "明确的余额不足提示", "weekly_error_code": "明确的周额度错误码", "weekly_error_message": "明确的周限提示", "codex_weekly_header": "Codex 七天用量窗口已耗尽", "anthropic_weekly_header": "Anthropic 七天用量窗口已耗尽"}
	if label, ok := labels[signal]; ok {
		return label
	}
	return "已识别的上游失败信号"
}
