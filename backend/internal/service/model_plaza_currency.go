package service

import (
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
