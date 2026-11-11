package service

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

type AutoCreditEmail struct {
	LedgerID       int64
	UserID         int64
	RecipientEmail string
	Amount         string
	BalanceAfter   string
}

// DeliverNext holds exclusive database ownership through delivery and ack.
// SMTP acceptance followed by a crash before ack can still cause a retry.
type AutoCreditEmailOutboxRepository interface {
	DeliverNext(context.Context, func(context.Context, AutoCreditEmail) error) (bool, error)
	Close() error
}

type AutoCreditEmailSender interface {
	Send(context.Context, NotificationEmailSendInput) error
}

type AutoCreditEmailWorker struct {
	repo    AutoCreditEmailOutboxRepository
	sender  AutoCreditEmailSender
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	stopped bool
	close   sync.Once
}

func NewAutoCreditEmailWorker(repo AutoCreditEmailOutboxRepository, sender AutoCreditEmailSender) *AutoCreditEmailWorker {
	return &AutoCreditEmailWorker{repo: repo, sender: sender}
}

func (w *AutoCreditEmailWorker) Start() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.cancel != nil || w.repo == nil || w.sender == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	go w.run(ctx)
}

func (w *AutoCreditEmailWorker) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.stopped = true
	if w.cancel != nil {
		w.cancel()
	}
	done := w.done
	w.mu.Unlock()
	if done != nil {
		<-done
	}
	w.close.Do(func() {
		if w.repo != nil {
			_ = w.repo.Close()
		}
	})
}

func (w *AutoCreditEmailWorker) run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		// Bound each drain so shutdown and other workers remain responsive.
		for range 20 {
			if ctx.Err() != nil {
				return
			}
			found, err := w.ProcessNext(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Error("auto credit email delivery failed; durable retry retained", "error", err)
			}
			if err != nil || !found {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *AutoCreditEmailWorker) ProcessNext(ctx context.Context) (bool, error) {
	return w.repo.DeliverNext(ctx, func(sendCtx context.Context, event AutoCreditEmail) error {
		// The locked ledger outbox owns deduplication and acknowledgment. Omit
		// the generic source marker: its post-SMTP settings write could fail on
		// normal shutdown after acceptance and cause an avoidable resend.
		return w.sender.Send(sendCtx, NotificationEmailSendInput{
			Event:          NotificationEmailEventAutoCreditSuccess,
			UserID:         event.UserID,
			RecipientEmail: event.RecipientEmail,
			RecipientName:  event.RecipientEmail,
			Variables:      map[string]string{"credit_amount": event.Amount, "current_balance": event.BalanceAfter, "ledger_id": strconv.FormatInt(event.LedgerID, 10)},
		})
	})
}
