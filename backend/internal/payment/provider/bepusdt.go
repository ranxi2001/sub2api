package provider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/shopspring/decimal"
)

// BEpusdt uses the native signed JSON API. Order amounts are fiat amounts;
// actual_amount is crypto and must never be used to credit the local order.
type BEpusdt struct {
	config     map[string]string
	httpClient *http.Client
}

func NewBEpusdt(_ string, config map[string]string) (*BEpusdt, error) {
	cfg := make(map[string]string, len(config))
	for key, value := range config {
		cfg[key] = strings.TrimSpace(value)
	}
	for _, key := range []string{"apiBase", "apiToken", "notifyUrl", "returnUrl"} {
		if cfg[key] == "" {
			return nil, fmt.Errorf("bepusdt config missing required key: %s", key)
		}
		if key != "apiToken" {
			if err := bepusdtValidateURL(cfg[key]); err != nil {
				return nil, fmt.Errorf("bepusdt invalid %s: %w", key, err)
			}
		}
	}
	base, _ := url.Parse(cfg["apiBase"])
	if base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("bepusdt apiBase must not contain a query or fragment")
	}
	cfg["apiBase"] = strings.TrimRight(cfg["apiBase"], "/")
	currency, err := payment.NormalizePaymentCurrency(cfg["currency"])
	if err != nil {
		return nil, err
	}
	switch currency {
	case "CNY", "USD", "EUR", "GBP", "JPY":
		cfg["currency"] = currency
	default:
		return nil, fmt.Errorf("bepusdt fiat currency must be CNY, USD, EUR, GBP or JPY")
	}
	return &BEpusdt{config: cfg, httpClient: &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (b *BEpusdt) Name() string                          { return "BEpusdt" }
func (b *BEpusdt) ProviderKey() string                   { return payment.TypeBEpusdt }
func (b *BEpusdt) SupportedTypes() []payment.PaymentType { return payment.BEpusdtTradeTypes() }

// Match upstream EpusdtSign, including Go's numeric formatting after JSON
// decoding (42.00 becomes 42, while string "4.250" stays "4.250").
func bepusdtSign(data map[string]any, token string) string {
	keys := make([]string, 0, len(data))
	for key, value := range data {
		if key != "signature" && value != nil && value != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", key, data[key]))
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + token))
	return hex.EncodeToString(sum[:])
}

func bepusdtScalarFields(raw []byte) (map[string]any, error) {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("bepusdt invalid JSON object")
	}
	for _, value := range fields {
		switch value.(type) {
		case nil, string, float64, bool:
		default:
			return nil, fmt.Errorf("bepusdt expected scalar JSON fields")
		}
	}
	return fields, nil
}

func bepusdtField(fields map[string]any, key string) string {
	if fields[key] == nil {
		return ""
	}
	return fmt.Sprint(fields[key])
}

func bepusdtPositiveAmount(raw string) (float64, error) {
	amount, err := strconv.ParseFloat(raw, 64)
	if err != nil || amount <= 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0, fmt.Errorf("bepusdt invalid payment amount")
	}
	return amount, nil
}

func bepusdtValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("expected an absolute HTTP(S) URL without credentials")
	}
	return nil
}

func (b *BEpusdt) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	if strings.TrimSpace(req.OrderID) == "" || !payment.IsBEpusdtTradeType(req.PaymentType) {
		return nil, fmt.Errorf("bepusdt invalid order ID or unsupported payment network")
	}
	amount, err := bepusdtPositiveAmount(req.Amount)
	if err != nil {
		return nil, err
	}
	if _, err := payment.AmountToMinorUnit(req.Amount, b.config["currency"]); err != nil {
		return nil, err
	}
	notifyURL, returnURL := b.config["notifyUrl"], b.config["returnUrl"]
	if req.NotifyURL != "" {
		notifyURL = req.NotifyURL
	}
	if req.ReturnURL != "" {
		returnURL = req.ReturnURL
	}
	for _, value := range []string{notifyURL, returnURL} {
		if err := bepusdtValidateURL(value); err != nil {
			return nil, err
		}
	}
	data := map[string]any{
		"order_id": req.OrderID, "amount": amount, "fiat": b.config["currency"],
		"trade_type": req.PaymentType, "name": req.Subject,
		"notify_url": notifyURL, "redirect_url": returnURL,
	}
	if !req.ExpiresAt.IsZero() {
		timeout := int64(math.Ceil(time.Until(req.ExpiresAt).Seconds()))
		if timeout <= 0 {
			return nil, fmt.Errorf("bepusdt order has already expired")
		}
		if timeout < 120 {
			timeout = 120
		}
		data["timeout"] = float64(timeout)
	}
	fields, err := b.post(ctx, "/api/v1/order/create-transaction", data, true)
	if err != nil {
		return nil, err
	}
	tradeID, payURL := bepusdtField(fields, "trade_id"), bepusdtField(fields, "payment_url")
	if tradeID == "" || bepusdtField(fields, "order_id") != req.OrderID || bepusdtField(fields, "fiat") != b.config["currency"] {
		return nil, fmt.Errorf("bepusdt response has inconsistent order identity or currency")
	}
	if upstreamType := bepusdtField(fields, "trade_type"); upstreamType != "" && upstreamType != req.PaymentType {
		return nil, fmt.Errorf("bepusdt response payment network mismatch")
	}
	expected, _ := decimal.NewFromString(req.Amount)
	actual, err := decimal.NewFromString(bepusdtField(fields, "amount"))
	if err != nil || !actual.Equal(expected) {
		return nil, fmt.Errorf("bepusdt response fiat amount mismatch")
	}
	if err := bepusdtValidateURL(payURL); err != nil {
		return nil, fmt.Errorf("bepusdt returned invalid checkout URL")
	}
	return &payment.CreatePaymentResponse{TradeNo: tradeID, PayURL: payURL, Currency: b.config["currency"]}, nil
}

func (b *BEpusdt) QueryOrder(ctx context.Context, tradeID string) (*payment.QueryOrderResponse, error) {
	if strings.TrimSpace(tradeID) == "" {
		return nil, fmt.Errorf("bepusdt trade ID is required")
	}
	fields, err := b.post(ctx, "/api/v1/pay/info", map[string]any{"trade_id": tradeID}, false)
	if err != nil {
		return nil, err
	}
	if bepusdtField(fields, "trade_id") != tradeID || bepusdtField(fields, "fiat") != b.config["currency"] {
		return nil, fmt.Errorf("bepusdt queried order identity or currency mismatch")
	}
	amount, err := bepusdtPositiveAmount(bepusdtField(fields, "money"))
	if err != nil {
		return nil, err
	}
	status := payment.ProviderStatusPending
	switch bepusdtField(fields, "status") {
	case "1", "5":
	case "2":
		status = payment.ProviderStatusPaid
	case "3", "4", "6":
		status = payment.ProviderStatusFailed
	default:
		return nil, fmt.Errorf("bepusdt unknown order status")
	}
	return &payment.QueryOrderResponse{TradeNo: tradeID, Status: status, Amount: amount,
		Metadata: map[string]string{"currency": b.config["currency"], "order_id": bepusdtField(fields, "order_id"), "trade_type": bepusdtField(fields, "trade_type")}}, nil
}

func (b *BEpusdt) VerifyNotification(_ context.Context, rawBody string, _ map[string]string) (*payment.PaymentNotification, error) {
	fields, err := bepusdtScalarFields([]byte(rawBody))
	if err != nil {
		return nil, err
	}
	provided := strings.ToLower(bepusdtField(fields, "signature"))
	expected := bepusdtSign(fields, b.config["apiToken"])
	if !hmac.Equal([]byte(provided), []byte(expected)) {
		return nil, fmt.Errorf("bepusdt invalid notification signature")
	}
	switch bepusdtField(fields, "status") {
	case "1", "3", "4", "5", "6":
		return nil, nil
	case "2":
	default:
		return nil, fmt.Errorf("bepusdt unknown notification status")
	}
	tradeID, orderID := bepusdtField(fields, "trade_id"), bepusdtField(fields, "order_id")
	if strings.TrimSpace(tradeID) == "" || strings.TrimSpace(orderID) == "" {
		return nil, fmt.Errorf("bepusdt notification missing order identity")
	}
	amount, err := bepusdtPositiveAmount(bepusdtField(fields, "amount"))
	if err != nil {
		return nil, err
	}
	if _, err := bepusdtPositiveAmount(bepusdtField(fields, "actual_amount")); err != nil {
		return nil, err
	}
	if fiat := bepusdtField(fields, "fiat"); fiat != "" && fiat != b.config["currency"] {
		return nil, fmt.Errorf("bepusdt notification fiat currency mismatch")
	}
	return &payment.PaymentNotification{TradeNo: tradeID, OrderID: orderID, Amount: amount,
		Status: payment.NotificationStatusSuccess, RawData: rawBody,
		Metadata: map[string]string{"currency": b.config["currency"], "order_id": orderID, "trade_type": bepusdtField(fields, "trade_type"), "block_transaction_id": bepusdtField(fields, "block_transaction_id")}}, nil
}

func (b *BEpusdt) CancelPayment(ctx context.Context, tradeID string) error {
	if strings.TrimSpace(tradeID) == "" {
		return fmt.Errorf("bepusdt trade ID is required")
	}
	_, err := b.post(ctx, "/api/v1/order/cancel-transaction", map[string]any{"trade_id": tradeID}, true)
	return err
}

func (b *BEpusdt) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	return nil, fmt.Errorf("bepusdt refunds are not supported")
}

func (b *BEpusdt) post(ctx context.Context, path string, fields map[string]any, signed bool) (map[string]any, error) {
	if signed {
		fields["signature"] = bepusdtSign(fields, b.config["apiToken"])
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.config["apiBase"]+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bepusdt request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bepusdt HTTP status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Code    int             `json:"status_code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("bepusdt invalid response JSON")
	}
	if envelope.Code != 200 {
		return nil, fmt.Errorf("bepusdt API error %d: %s", envelope.Code, envelope.Message)
	}
	return bepusdtScalarFields(envelope.Data)
}
