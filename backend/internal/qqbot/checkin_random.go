package qqbot

import (
	"crypto/rand"
	"errors"
	"math"
	"math/big"
)

const (
	checkinAmountScale      = 1e8
	checkinProbabilityTotal = 10000
)

// CheckinRewardDistribution belongs to the administrator configuration only.
// Never include it in user DTOs, command replies, or public activity rules.
type CheckinRewardDistribution struct {
	LowerHalfBPS    int `json:"lower_half_bps"`
	NextQuarterBPS  int `json:"next_quarter_bps"`
	UpperQuarterBPS int `json:"upper_quarter_bps"`
}

func (c CheckinConfig) rewardDistribution() CheckinRewardDistribution {
	if c.RewardDistribution != nil {
		return *c.RewardDistribution
	}
	return CheckinRewardDistribution{LowerHalfBPS: 7000, NextQuarterBPS: 2000, UpperQuarterBPS: 1000}
}

func validateCheckinDistribution(d CheckinRewardDistribution) error {
	total := 0
	for _, weight := range []int{d.LowerHalfBPS, d.NextQuarterBPS, d.UpperQuarterBPS} {
		if weight < 0 || weight > checkinProbabilityTotal {
			return errors.New("签到各区间概率需在 0% 到 100% 之间，最多两位小数")
		}
		total += weight
	}
	if total != checkinProbabilityTotal {
		return errors.New("签到三个区间的概率合计必须为 100%")
	}
	return nil
}

type checkinAmountBand struct {
	lo, hi int64
	weight int
}

func checkinAmountBands(c CheckinConfig) [3]checkinAmountBand {
	lo, hi := checkinAmountBounds(c)
	span := hi - lo
	// Boundaries belong to the following band. Integer arithmetic avoids
	// floating-point gaps and preserves inclusive endpoints at 1e-8 USD.
	middle, upper := lo+(span+1)/2, lo+(3*span+3)/4
	d := c.rewardDistribution()
	bands := [3]checkinAmountBand{
		{lo, middle - 1, d.LowerHalfBPS},
		{middle, upper - 1, d.NextQuarterBPS},
		{upper, hi, d.UpperQuarterBPS},
	}
	for i := range bands {
		// With fewer than three representable amounts, an empty band maps
		// to its nearest available amount rather than dropping its weight.
		if bands[i].hi < bands[i].lo {
			bands[i].hi = bands[i].lo
		}
	}
	return bands
}

func randomCheckinAmount(c CheckinConfig) (float64, error) {
	return randomCheckinAmountWith(c, func(limit int64) (int64, error) {
		n, err := rand.Int(rand.Reader, big.NewInt(limit))
		if err != nil {
			return 0, err
		}
		return n.Int64(), nil
	})
}

// Select a band using the configured probabilities, then sample uniformly
// within that band. draw must return an independent uniform integer in [0, limit).
func randomCheckinAmountWith(c CheckinConfig, draw func(limit int64) (int64, error)) (float64, error) {
	if err := validateCheckin(c); err != nil {
		return 0, err
	}
	lo, hi := checkinAmountBounds(c)
	if lo == hi {
		return float64(lo) / checkinAmountScale, nil
	}
	roll, err := draw(checkinProbabilityTotal)
	if err != nil {
		return 0, err
	}
	for _, band := range checkinAmountBands(c) {
		roll -= int64(band.weight)
		if roll < 0 {
			offset, err := draw(band.hi - band.lo + 1)
			if err != nil {
				return 0, err
			}
			return float64(band.lo+offset) / checkinAmountScale, nil
		}
	}
	return 0, errors.New("checkin roll outside validated distribution")
}

func checkinAmountBounds(c CheckinConfig) (int64, int64) {
	return int64(math.Round(c.MinAmount * checkinAmountScale)), int64(math.Round(c.MaxAmount * checkinAmountScale))
}

func checkinExpectedAmount(c CheckinConfig) float64 {
	lo, hi := checkinAmountBounds(c)
	if lo == hi {
		return float64(lo) / checkinAmountScale
	}
	var mean float64
	for _, band := range checkinAmountBands(c) {
		mean += (float64(band.lo) + float64(band.hi)) / 2 * float64(band.weight) / checkinProbabilityTotal
	}
	return mean / checkinAmountScale
}
