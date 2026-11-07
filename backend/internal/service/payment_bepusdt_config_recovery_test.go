//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestBEpusdtExpiredRecoveryProtectsProviderConfiguration(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := NewPaymentConfigService(client, nil, nil)
	// Disabled drafts avoid starting the collection engine in this config test.
	instance, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey: payment.TypeBEpusdt, Name: "Recovery provider", Enabled: false,
		SupportedTypes: []string{"usdt.bep20"},
		Config:         map[string]string{"mode": "embedded", "currency": "USD"},
	})
	require.NoError(t, err)
	createPendingProviderConfigOrder(t, ctx, client, instance)
	order, err := client.PaymentOrder.Query().Only(ctx)
	require.NoError(t, err)
	_, err = client.PaymentOrder.UpdateOneID(order.ID).
		SetPaymentType("usdt.bep20").SetStatus(payment.OrderStatusExpired).
		SetExpiresAt(time.Now().Add(-10 * time.Minute)).
		SetUpdatedAt(time.Now().Add(-10 * time.Minute)).
		SetProviderSnapshot(map[string]any{"collection_mode": "embedded"}).Save(ctx)
	require.NoError(t, err)

	for _, change := range []map[string]string{
		{"mode": "external"}, {"currency": "CNY"},
		{"apiToken": "replacement-token"}, {"apiBase": "https://gateway.example"},
	} {
		_, err = svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{Config: change})
		require.ErrorContains(t, err, "pending orders")
	}
	_, err = svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{SupportedTypes: []string{}})
	require.ErrorContains(t, err, "pending orders")
	disabled := false
	_, err = svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{Enabled: &disabled})
	require.ErrorContains(t, err, "pending orders")
	require.ErrorContains(t, svc.DeleteProviderInstance(ctx, instance.ID), "in-progress orders")

	name := "Renamed recovery provider"
	_, err = svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{Name: &name})
	require.NoError(t, err, "presentation changes must remain available")

	_, err = client.PaymentOrder.UpdateOneID(order.ID).
		SetUpdatedAt(time.Now().Add(-bepusdtOrderRecoveryWindow - time.Minute)).Save(ctx)
	require.NoError(t, err)
	_, err = svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{
		Config: map[string]string{"mode": "external", "currency": "CNY"}, SupportedTypes: []string{},
	})
	require.NoError(t, err, "protection must end with the merchant recovery window")
	require.NoError(t, svc.DeleteProviderInstance(ctx, instance.ID))
}

func TestBEpusdtRecoveryProtectionMatchesOrderStateAndCollectionMode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   string
		mode     string
		age      time.Duration
		wantLock bool
	}{
		{"recent embedded expiry", payment.OrderStatusExpired, "embedded", 2*time.Hour + 59*time.Minute, true},
		{"old embedded expiry", payment.OrderStatusExpired, "embedded", 3*time.Hour + time.Minute, false},
		{"external expiry", payment.OrderStatusExpired, "external", time.Minute, false},
		{"legacy external expiry", payment.OrderStatusExpired, "", time.Minute, false},
		{"completed embedded order", payment.OrderStatusCompleted, "embedded", time.Minute, false},
		{"pending external order", payment.OrderStatusPending, "external", 4 * time.Hour, true},
		{"paid embedded order", payment.OrderStatusPaid, "embedded", 4 * time.Hour, true},
		{"recharging embedded order", payment.OrderStatusRecharging, "embedded", 4 * time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			svc := NewPaymentConfigService(client, nil, nil)
			instance, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
				ProviderKey: payment.TypeBEpusdt, Name: "Recovery provider", Enabled: false,
				SupportedTypes: []string{"usdt.bep20"}, Config: map[string]string{"mode": tc.mode},
			})
			require.NoError(t, err)
			createPendingProviderConfigOrder(t, ctx, client, instance)
			order, err := client.PaymentOrder.Query().Only(ctx)
			require.NoError(t, err)
			_, err = client.PaymentOrder.UpdateOneID(order.ID).
				SetStatus(tc.status).SetUpdatedAt(time.Now().Add(-tc.age)).
				SetProviderSnapshot(map[string]any{"collection_mode": tc.mode}).Save(ctx)
			require.NoError(t, err)
			count, err := svc.countPendingOrders(ctx, instance.ID)
			require.NoError(t, err)
			if tc.wantLock {
				require.Equal(t, 1, count)
			} else {
				require.Zero(t, count)
			}
		})
	}
}
