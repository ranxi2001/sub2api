package qqbot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReplyBudgetIncludesQueueTimeAndPreservesDelivery(t *testing.T) {
	created := time.Now().Add(-200 * time.Second)
	ctx, cancel := jobContext(context.Background(), Job{CreatedAt: created, EventType: groupAtMessageCreate})
	defer cancel()
	deadline, _ := ctx.Deadline()
	require.Equal(t, created.Add(passiveReplyWindow), deadline)
	model, stop, err := modelContext(ctx)
	require.NoError(t, err)
	defer stop()
	modelDeadline, _ := model.Deadline()
	require.Equal(t, deadline.Add(-replyDeliveryReserve), modelDeadline)
	stop()
	require.NoError(t, ctx.Err(), "generation timeout must leave delivery usable")
}

func TestReplyBudgetRejectsPaidWorkBeforeQuotaAndHTTP(t *testing.T) {
	ctx, cancel := jobContext(context.Background(), Job{CreatedAt: time.Now().Add(-250 * time.Second), EventType: groupMessageCreate})
	defer cancel()
	s := &Service{} // nil DB/client proves the check precedes both side effects
	require.ErrorIs(t, s.reserveUsage(ctx, Config{}, Job{}, "chat"), errReplyWindow)
	_, _, err := s.chatRequestWithToolChoice(ctx, Config{}, nil, nil, "auto")
	require.ErrorIs(t, err, errReplyWindow)
	_, err = s.generateImage(ctx, Config{}, "image")
	require.ErrorIs(t, err, errReplyWindow)
}

func TestReplyBudgetFreshRequestAndNonMessageEvents(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte("ok")) }))
	defer server.Close()
	s := &Service{http: server.Client()}
	for _, event := range []string{groupMessageCreate, interactionCreate, rankingNotificationEvent, "GROUP_MEMBER_ADD"} {
		created := time.Now()
		if event == rankingNotificationEvent || event == "GROUP_MEMBER_ADD" {
			created = created.Add(-time.Hour)
		}
		ctx, cancel := jobContext(context.Background(), Job{CreatedAt: created, EventType: event})
		_, _, err := s.requestBody(ctx, http.MethodPost, server.URL, "", nil, "", 100)
		require.NoError(t, err, event)
		cancel()
		require.ErrorIs(t, checkModelBudget(ctx, minimumModelBudget), context.Canceled)
	}
	require.Equal(t, 4, calls)
}

func TestImageBudgetDoesNotStartAfterChatUsedWindow(t *testing.T) {
	ctx, cancel := jobContext(context.Background(), Job{CreatedAt: time.Now().Add(-215 * time.Second), EventType: groupMessageCreate})
	defer cancel()
	require.NoError(t, checkModelBudget(ctx, minimumModelBudget))
	s := &Service{}
	require.ErrorIs(t, s.reserveUsage(ctx, Config{}, Job{}, "image"), errReplyWindow)
	_, err := s.editImage(ctx, Config{}, "edit", "invalid")
	require.ErrorIs(t, err, errReplyWindow)
}

func TestReplyBudgetDoesNotConfuseAttemptTimeoutWithReplyWindow(t *testing.T) {
	ctx, cancel := jobContext(context.Background(), Job{CreatedAt: time.Now(), EventType: groupMessageCreate})
	defer cancel()
	attempt, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	model, done, err := modelContext(attempt)
	require.NoError(t, err)
	defer done()
	expected, _ := attempt.Deadline()
	actual, _ := model.Deadline()
	require.Equal(t, expected, actual, "short attempt deadline must still be enforced")
}
