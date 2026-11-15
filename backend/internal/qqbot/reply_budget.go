package qqbot

import (
	"context"
	"errors"
	"time"
)

const (
	passiveReplyWindow   = 280 * time.Second
	replyDeliveryReserve = 25 * time.Second
	minimumModelBudget   = 15 * time.Second
	minimumImageBudget   = 45 * time.Second
)

var errReplyWindow = errors.New("剩余回复时间不足，请重新发送")

type replyDeadlineKey struct{}

// Queueing, model calls, uploads and delivery share the original event window.
// Membership events and proactive notifications use their own lifecycle.
func jobContext(parent context.Context, j Job) (context.Context, context.CancelFunc) {
	if isGroupMessageEvent(j.EventType) || j.EventType == interactionCreate {
		deadline := j.CreatedAt.Add(passiveReplyWindow)
		ctx := context.WithValue(parent, replyDeadlineKey{}, deadline)
		return context.WithDeadline(ctx, deadline)
	}
	return context.WithTimeout(parent, 260*time.Second)
}

func checkModelBudget(ctx context.Context, minimum time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Value(replyDeadlineKey{}).(time.Time); ok {
		deadline = deadline.Add(-replyDeliveryReserve)
		// A retry or stage may intentionally use a shorter request timeout. That
		// timeout still cancels the HTTP call, but must not masquerade as expiry
		// of the original QQ reply window.
		if time.Until(deadline) < minimum {
			return errReplyWindow
		}
	}
	return nil
}

func modelContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := checkModelBudget(ctx, minimumModelBudget); err != nil {
		return nil, nil, err
	}
	if deadline, ok := ctx.Value(replyDeadlineKey{}).(time.Time); ok {
		child, cancel := context.WithDeadline(ctx, deadline.Add(-replyDeliveryReserve))
		return child, cancel, nil
	}
	return ctx, func() {}, nil
}
