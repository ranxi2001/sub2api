package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/bepusdt"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type CryptoCheckout struct {
	OrderID         int64     `json:"order_id"`
	OutTradeNo      string    `json:"out_trade_no"`
	Status          string    `json:"status"`
	Network         string    `json:"network"`
	Crypto          string    `json:"crypto"`
	Address         string    `json:"address"`
	CryptoAmount    string    `json:"crypto_amount"`
	FiatAmount      string    `json:"fiat_amount"`
	Currency        string    `json:"currency"`
	ExpiresAt       time.Time `json:"expires_at"`
	Confirming      bool      `json:"confirming"`
	TransactionHash string    `json:"transaction_hash,omitempty"`
	NetworkStatus   string    `json:"network_status"`
}

// Resolve the merchant order through authenticated ownership before touching
// the collection DB. A trade ID or another user's order cannot read a checkout.
func (s *PaymentService) GetCryptoCheckout(ctx context.Context, outTradeNo string, userID int64) (*CryptoCheckout, error) {
	order, err := s.entClient.PaymentOrder.Query().Where(paymentorder.OutTradeNo(outTradeNo), paymentorder.UserID(userID)).Only(ctx)
	if err != nil {
		return nil, infraerrors.NotFound("ORDER_NOT_FOUND", "payment order not found")
	}
	if order.ProviderKey == nil || *order.ProviderKey != payment.TypeBEpusdt || order.PaymentTradeNo == "" {
		return nil, infraerrors.NotFound("ORDER_NOT_FOUND", "built-in payment order not found")
	}
	instance, err := s.getOrderProviderInstance(ctx, order)
	if err != nil {
		return nil, err
	}
	if instance == nil {
		return nil, infraerrors.NotFound("ORDER_NOT_FOUND", "payment provider not found")
	}
	mode := psSnapshotStringValue(order.ProviderSnapshot["collection_mode"])
	if mode == "" {
		cfg, err := s.configService.decryptConfig(instance.Config)
		if err != nil {
			return nil, err
		}
		mode = cfg["mode"]
	}
	if mode != "embedded" {
		return nil, infraerrors.NotFound("ORDER_NOT_FOUND", "built-in payment order not found")
	}
	trade, err := bepusdt.Order(ctx, order.PaymentTradeNo)
	if err != nil {
		return nil, err
	}
	if trade.OrderId != outTradeNo || string(trade.TradeType) != order.PaymentType || string(trade.Fiat) != PaymentOrderCurrency(order) {
		return nil, fmt.Errorf("built-in payment identity mismatch")
	}
	actual, actualErr := payment.AmountToMinorUnit(trade.Money, string(trade.Fiat))
	expected, expectedErr := payment.AmountToMinorUnit(strconv.FormatFloat(order.PayAmount, 'f', -1, 64), string(trade.Fiat))
	if actualErr != nil || expectedErr != nil || actual != expected {
		return nil, fmt.Errorf("built-in payment amount mismatch")
	}
	return &CryptoCheckout{OrderID: order.ID, OutTradeNo: outTradeNo, Status: order.Status,
		Network: string(trade.TradeType), Crypto: string(trade.Crypto), Address: trade.Address,
		CryptoAmount: trade.Amount, FiatAmount: trade.Money, Currency: string(trade.Fiat),
		ExpiresAt: trade.ExpiredAt, Confirming: trade.Status == 5, TransactionHash: trade.RefHash,
		NetworkStatus: bepusdt.PaymentNetworkStatus(string(trade.TradeType))}, nil
}
