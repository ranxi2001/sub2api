package service

import (
	"encoding/json"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// These are upstream display settings, independent of the account's fixed
// procurement-cost conversion and of downstream billing.
type newAPIWalletSettings struct {
	QuotaPerUnit               *float64 `json:"quota_per_unit"`
	QuotaDisplayType           string   `json:"quota_display_type"`
	USDExchangeRate            *float64 `json:"usd_exchange_rate"`
	CustomCurrencySymbol       string   `json:"custom_currency_symbol"`
	CustomCurrencyExchangeRate *float64 `json:"custom_currency_exchange_rate"`
}

// A malformed optional display setting must not invalidate a verified wallet.
// QuotaPerUnit remains strict because it defines the underlying ledger unit.
func (s *newAPIWalletSettings) UnmarshalJSON(data []byte) error {
	var raw struct {
		QuotaPerUnit               *float64        `json:"quota_per_unit"`
		QuotaDisplayType           json.RawMessage `json:"quota_display_type"`
		USDExchangeRate            json.RawMessage `json:"usd_exchange_rate"`
		CustomCurrencySymbol       json.RawMessage `json:"custom_currency_symbol"`
		CustomCurrencyExchangeRate json.RawMessage `json:"custom_currency_exchange_rate"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = newAPIWalletSettings{QuotaPerUnit: raw.QuotaPerUnit}
	_ = json.Unmarshal(raw.QuotaDisplayType, &s.QuotaDisplayType)
	_ = json.Unmarshal(raw.USDExchangeRate, &s.USDExchangeRate)
	_ = json.Unmarshal(raw.CustomCurrencySymbol, &s.CustomCurrencySymbol)
	_ = json.Unmarshal(raw.CustomCurrencyExchangeRate, &s.CustomCurrencyExchangeRate)
	return nil
}

func (s newAPIWalletSettings) displayWallet(raw float64) (amount float64, unit string, rate float64) {
	amount, unit, rate = raw, "USD", 1
	var factor *float64
	var currency string
	switch strings.ToUpper(strings.TrimSpace(s.QuotaDisplayType)) {
	case "CNY":
		factor, currency = s.USDExchangeRate, "CNY"
	case "CUSTOM":
		factor, currency = s.CustomCurrencyExchangeRate, strings.TrimSpace(s.CustomCurrencySymbol)
		if !utf8.ValidString(currency) || len(currency) == 0 || len(currency) > 32 || strings.IndexFunc(currency, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
			return
		}
		// Do not assign an arbitrary custom rate to the legacy USD unit.
		if strings.EqualFold(currency, "USD") {
			return
		}
	default:
		// Older servers and token/unknown display modes retain a labelled USD
		// wallet instead of guessing a currency or hiding a valid balance.
		return
	}
	if factor == nil || !newAPIFinite(*factor) || *factor <= 0 || *factor > 1e6 {
		return
	}
	converted, _ := decimal.NewFromFloat(raw).Mul(decimal.NewFromFloat(*factor)).Float64()
	if !newAPIFinite(converted) || math.Abs(converted) > 1e12 {
		return
	}
	return converted, currency, *factor
}
