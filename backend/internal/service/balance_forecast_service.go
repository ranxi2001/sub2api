package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/balanceforecast"
)

const (
	forecastSweepInterval   = 5 * time.Minute
	forecastPageSize        = 100
	forecastPagesPerSweep   = 20
	forecastSweepCandidates = forecastPageSize * forecastPagesPerSweep
)

// BalanceForecastService runs only on full nodes. Claims are durable across
// instances; no SMTP calls execute inside a transaction or request billing path.
type BalanceForecastService struct {
	store    *balanceforecast.Store
	settings SettingRepository
	users    interface {
		GetByID(context.Context, int64) (*User, error)
	}
	mail   *NotificationEmailService
	now    func() time.Time
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewBalanceForecastService(db *sql.DB, settings SettingRepository, users interface {
	GetByID(context.Context, int64) (*User, error)
}, mail *NotificationEmailService) *BalanceForecastService {
	return &BalanceForecastService{store: &balanceforecast.Store{DB: db}, settings: settings, users: users, mail: mail, now: time.Now}
}
func (s *BalanceForecastService) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		// Delayed first sweep keeps enabling/saving settings free of side effects.
		ticker := time.NewTicker(forecastSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCtx, stop := context.WithTimeout(ctx, 4*time.Minute)
				if err := s.RunOnce(runCtx); err != nil && ctx.Err() == nil {
					slog.Warn("balance forecast sweep failed", "error", err)
				}
				stop()
			}
		}
	}()
}
func (s *BalanceForecastService) Stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}
func (s *BalanceForecastService) config(ctx context.Context) (SystemSettings, error) {
	values, err := s.settings.GetMultiple(ctx, []string{SettingKeyBalanceForecastEnabled, SettingKeyBalanceForecastThresholdHours, SettingKeyBalanceForecastMaxNotifications})
	var cfg SystemSettings
	if err == nil {
		parseBalanceForecastSettings(values, &cfg)
	}
	return cfg, err
}
func (s *BalanceForecastService) RunOnce(ctx context.Context) error {
	cfg, err := s.config(ctx)
	if err != nil {
		return err
	}
	if !cfg.BalanceForecastEnabled {
		return nil
	}
	if s.mail == nil || s.users == nil || s.store == nil || s.store.DB == nil {
		return errors.New("forecast dependencies unavailable")
	}
	recoveryGap, err := s.store.RecoveryObservationGap(ctx, forecastSweepCandidates, forecastSweepInterval)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	var after int64
	// At most 2,000 users per sweep. Rotate the cursor persistently so large
	// installations do not repeatedly starve IDs after the first 2,000 users.
	raw, err := s.settings.GetValue(ctx, "balance_forecast_cursor")
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return err
	}
	if raw != "" {
		if _, err = fmt.Sscan(raw, &after); err != nil {
			return err
		}
	}
	for page := 0; page < forecastPagesPerSweep; page++ {
		candidates, err := s.store.Candidates(ctx, now, after, forecastPageSize)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return s.settings.Set(ctx, "balance_forecast_cursor", "0")
		}
		for _, c := range candidates {
			if err = s.process(ctx, c, now, cfg, recoveryGap); err != nil {
				return err
			}
			after = c.ID
		}
		if err = s.settings.Set(ctx, "balance_forecast_cursor", fmt.Sprint(after)); err != nil {
			return err
		}
	}
	return nil
}

var errForecastSuppressed = errors.New("forecast suppressed before SMTP")

func (s *BalanceForecastService) process(ctx context.Context, c balanceforecast.Candidate, now time.Time, cfg SystemSettings, recoveryGap time.Duration) error {
	if !c.Reliable() {
		return nil
	}
	// Recovery is observed even for opted-out/unreachable recipients.
	if c.Hours > cfg.BalanceForecastThresholdHours {
		_, err := s.store.ClaimWithRecoveryGap(ctx, c, now, cfg.BalanceForecastThresholdHours, cfg.BalanceForecastMaxNotifications, recoveryGap)
		return err
	}
	if err := s.store.InterruptRecovery(ctx, c.ID); err != nil {
		return err
	}
	user, err := s.users.GetByID(ctx, c.ID)
	if err != nil {
		return err
	}
	recipients, err := s.recipients(ctx, user, now)
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return nil
	}
	if s.mail.emailService == nil {
		return nil
	}
	if _, err := s.mail.emailService.GetSMTPConfig(ctx); err != nil {
		return err
	}
	id, err := s.store.ClaimWithRecoveryGap(ctx, c, now, cfg.BalanceForecastThresholdHours, cfg.BalanceForecastMaxNotifications, recoveryGap)
	if err != nil || id == 0 {
		return err
	}
	state := "skipped"
	for _, to := range recipients {
		// Never invoke the fallback mail path after an ambiguous SMTP error.
		attempted := false
		err = s.mail.Send(ctx, NotificationEmailSendInput{
			Event: NotificationEmailEventBalanceForecast, RecipientEmail: to, RecipientName: user.Username, UserID: c.ID,
			SourceType: "balance_forecast", SourceID: fmt.Sprint(c.ID), ReminderKey: fmt.Sprint(id),
			Variables: map[string]string{"current_balance": fmt.Sprintf("%.2f", c.Balance), "hourly_spend": fmt.Sprintf("%.4f", c.Hourly), "hours_remaining": fmt.Sprintf("%.1f", c.Hours), "observation_hours": fmt.Sprintf("%.1f", c.ObservationHours), "recharge_url": s.rechargeURL(ctx)},
			beforeSend: func(sendCtx context.Context) error {
				current, checkErr := s.config(sendCtx)
				if checkErr != nil {
					return checkErr
				}
				if !current.BalanceForecastEnabled || current.BalanceForecastMaxNotifications < cfg.BalanceForecastMaxNotifications {
					return errForecastSuppressed
				}
				fresh, checkErr := s.users.GetByID(sendCtx, c.ID)
				if checkErr != nil {
					return checkErr
				}
				allowed, checkErr := s.recipients(sendCtx, fresh, now)
				if checkErr != nil {
					return checkErr
				}
				found := false
				for _, email := range allowed {
					if email == to {
						found = true
					}
				}
				if !found {
					return errForecastSuppressed
				}
				snapshot, checkErr := s.store.Candidates(sendCtx, s.now().UTC(), c.ID-1, 1)
				if checkErr != nil {
					return checkErr
				}
				if len(snapshot) != 1 || snapshot[0].ID != c.ID || !snapshot[0].Reliable() || snapshot[0].Hours > current.BalanceForecastThresholdHours {
					return errForecastSuppressed
				}
				attempted = true
				return nil
			},
		})
		if attempted {
			if err != nil {
				state = "uncertain"
			} else if state != "uncertain" {
				state = "sent"
			}
		}
		if err != nil && !errors.Is(err, errForecastSuppressed) {
			slog.Warn("forecast notification not confirmed", "user_id", c.ID, "outbox_id", id)
		}
	}
	// A canceled send keeps its reservation if recording the result is impossible.
	return s.store.Finish(ctx, id, state, s.now().UTC())
}
func (s *BalanceForecastService) rechargeURL(ctx context.Context) string {
	value, err := s.settings.GetValue(ctx, SettingKeyBalanceLowNotifyRechargeURL)
	if err != nil {
		return ""
	}
	return value
}
func (s *BalanceForecastService) recipients(ctx context.Context, user *User, now time.Time) ([]string, error) {
	if user == nil || !user.BalanceNotifyEnabled || user.Status != StatusActive {
		return nil, nil
	}
	var result []string
	seen := map[string]bool{}
	for _, to := range filterVerifiedEmails(user.BalanceNotifyExtraEmails) {
		if seen[to] {
			continue
		}
		seen[to] = true
		for _, event := range []string{NotificationEmailEventBalanceLow, NotificationEmailEventBalanceForecast} {
			off, err := s.mail.IsUnsubscribed(ctx, to, event)
			if err != nil {
				return nil, err
			}
			if off {
				seen[to] = false
				break
			}
		}
		if !seen[to] {
			continue
		}
		// The older amount alert remains independent; suppress an additional
		// prediction to a recipient already alerted by it on this UTC day.
		key := notificationEmailDeliveryKey(NotificationEmailEventBalanceLow, "balance_low", fmt.Sprint(user.ID), to, now.UTC().Format("2006-01-02"))
		legacy := legacyNotificationEmailDeliveryKey(NotificationEmailEventBalanceLow, "balance_low", fmt.Sprint(user.ID), to, now.UTC().Format("2006-01-02"))
		sent, err := s.mail.deliveryExists(ctx, key, legacy)
		if err != nil {
			return nil, err
		}
		if !sent {
			result = append(result, to)
		}
	}
	return result, nil
}
