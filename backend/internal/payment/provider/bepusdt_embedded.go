package provider

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/bepusdt"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/v03413/bepusdt/app/model"
)

// EmbeddedBEpusdt invokes the compiled collection engine directly; no gateway
// server, API address or administrator-provided API token is involved.
type EmbeddedBEpusdt struct{ *BEpusdt }

func NewEmbeddedBEpusdt(instanceID string, config map[string]string) (*EmbeddedBEpusdt, error) {
	token, err := bepusdt.Token()
	if err != nil {
		return nil, fmt.Errorf("initialize built-in BEpusdt: %w", err)
	}
	cfg := make(map[string]string, len(config)+2)
	for key, value := range config {
		cfg[key] = value
	}
	cfg["apiBase"], cfg["apiToken"] = "http://embedded.invalid", token
	b, err := NewBEpusdt(instanceID, cfg)
	if err != nil {
		return nil, err
	}
	return &EmbeddedBEpusdt{BEpusdt: b}, nil
}

func (b *EmbeddedBEpusdt) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	if req.OrderID == "" || !payment.IsBEpusdtTradeType(req.PaymentType) {
		return nil, fmt.Errorf("invalid built-in payment order or network")
	}
	if _, err := payment.AmountToMinorUnit(req.Amount, b.config["currency"]); err != nil {
		return nil, err
	}
	if !req.ExpiresAt.IsZero() && time.Until(req.ExpiresAt) <= 0 {
		return nil, fmt.Errorf("payment order has expired")
	}
	notifyURL, returnURL := b.config["notifyUrl"], b.config["returnUrl"]
	if req.NotifyURL != "" {
		notifyURL = req.NotifyURL
	}
	if req.ReturnURL != "" {
		returnURL = req.ReturnURL
	}
	if err := bepusdtValidateURL(notifyURL); err != nil {
		return nil, err
	}
	if err := bepusdtValidateURL(returnURL); err != nil {
		return nil, err
	}
	order, err := bepusdt.Create(ctx, bepusdt.CreateRequest{OrderID: req.OrderID, Amount: req.Amount,
		TradeType: req.PaymentType, Currency: b.config["currency"], Subject: req.Subject,
		NotifyURL: notifyURL, ReturnURL: returnURL, ExpiresAt: req.ExpiresAt})
	if err != nil {
		return nil, err
	}
	checkout, _ := url.Parse(returnURL)
	checkout.Path = "/payment/crypto/" + req.OrderID
	checkout.RawPath, checkout.RawQuery, checkout.Fragment = "", "", ""
	return &payment.CreatePaymentResponse{TradeNo: order.TradeId, PayURL: checkout.String(), Currency: b.config["currency"]}, nil
}

func (b *EmbeddedBEpusdt) QueryOrder(ctx context.Context, tradeID string) (*payment.QueryOrderResponse, error) {
	order, err := bepusdt.Order(ctx, tradeID)
	if err != nil {
		return nil, err
	}
	if string(order.Fiat) != b.config["currency"] {
		return nil, fmt.Errorf("bepusdt currency mismatch")
	}
	amount, err := strconv.ParseFloat(order.Money, 64)
	if err != nil {
		return nil, err
	}
	status := payment.ProviderStatusPending
	switch order.Status {
	case model.OrderStatusWaiting, model.OrderStatusConfirming:
	case model.OrderStatusSuccess:
		status = payment.ProviderStatusPaid
	case model.OrderStatusCanceled, model.OrderStatusExpired, model.OrderStatusFailed:
		status = payment.ProviderStatusFailed
	default:
		return nil, fmt.Errorf("unknown built-in payment status")
	}
	return &payment.QueryOrderResponse{TradeNo: order.TradeId, Amount: amount, Status: status,
		Metadata: map[string]string{"currency": string(order.Fiat), "order_id": order.OrderId, "trade_type": string(order.TradeType)}}, nil
}

func (b *EmbeddedBEpusdt) CancelPayment(ctx context.Context, tradeID string) error {
	return bepusdt.Cancel(ctx, tradeID)
}
