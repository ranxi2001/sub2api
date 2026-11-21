package service

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// A configurable display reference, not a live FX quote or a billing rate.
const defaultModelPlazaUSDToCNYRate = 7.0

func modelPlazaUSDToCNYRate(raw string) float64 {
	rate, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return defaultModelPlazaUSDToCNYRate
	}
	return rate
}

// ModelPlazaCurrencySettings describes display values only. It does not configure payments.
type ModelPlazaCurrencySettings struct {
	WalletCurrency             string            `json:"wallet_currency"`
	WalletUnitValue            float64           `json:"wallet_unit_value"`
	CustomerCurrency           string            `json:"customer_currency"`
	OfficialCurrency           string            `json:"official_currency"`
	OfficialPlatformCurrencies map[string]string `json:"official_platform_currencies"`
	OfficialModelCurrencies    map[string]string `json:"official_model_currencies"`
}

func NormalizeModelPlazaCurrency(v ModelPlazaCurrencySettings) (ModelPlazaCurrencySettings, error) {
	if strings.TrimSpace(v.WalletCurrency) != "" && v.WalletUnitValue == 0 {
		return v, fmt.Errorf("wallet reference value must be positive when its currency is specified")
	}
	currency := func(raw string) (string, error) {
		value := strings.ToUpper(strings.TrimSpace(raw))
		if value == "" {
			value = "USD"
		}
		if value != "USD" && value != "CNY" {
			return "", fmt.Errorf("unsupported display currency %q", value)
		}
		return value, nil
	}
	var err error
	if v.WalletCurrency, err = currency(v.WalletCurrency); err != nil {
		return v, err
	}
	if v.CustomerCurrency, err = currency(v.CustomerCurrency); err != nil {
		return v, err
	}
	if v.OfficialCurrency, err = currency(v.OfficialCurrency); err != nil {
		return v, err
	}
	if v.WalletUnitValue == 0 {
		v.WalletUnitValue = 1
	}
	if v.WalletUnitValue < 0 || math.IsNaN(v.WalletUnitValue) || math.IsInf(v.WalletUnitValue, 0) {
		return v, fmt.Errorf("wallet reference value must be finite and positive")
	}
	normalizeOverrides := func(input map[string]string) (map[string]string, error) {
		if len(input) > 1000 {
			return nil, fmt.Errorf("too many currency overrides")
		}
		out := make(map[string]string, len(input))
		for key, raw := range input {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if normalized == "" || len(normalized) > 256 {
				return nil, fmt.Errorf("invalid currency override key")
			}
			if _, exists := out[normalized]; exists {
				return nil, fmt.Errorf("duplicate currency override key")
			}
			if strings.TrimSpace(raw) == "" {
				return nil, fmt.Errorf("currency override requires a currency")
			}
			value, err := currency(raw)
			if err != nil {
				return nil, err
			}
			out[normalized] = value
		}
		return out, nil
	}
	if v.OfficialPlatformCurrencies, err = normalizeOverrides(v.OfficialPlatformCurrencies); err != nil {
		return v, err
	}
	if v.OfficialModelCurrencies, err = normalizeOverrides(v.OfficialModelCurrencies); err != nil {
		return v, err
	}
	return v, nil
}

func parseModelPlazaCurrency(raw string) ModelPlazaCurrencySettings {
	var v ModelPlazaCurrencySettings
	if strings.TrimSpace(raw) != "" && json.Unmarshal([]byte(raw), &v) != nil {
		v = ModelPlazaCurrencySettings{}
	}
	normalized, err := NormalizeModelPlazaCurrency(v)
	if err != nil {
		normalized, _ = NormalizeModelPlazaCurrency(ModelPlazaCurrencySettings{})
	}
	return normalized
}
