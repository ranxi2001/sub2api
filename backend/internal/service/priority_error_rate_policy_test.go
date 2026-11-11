//go:build unit

package service

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPriorityErrorRateConfigBounds(t *testing.T) {
	for _, change := range []func(*PrioritySchedulingConfig){
		func(c *PrioritySchedulingConfig) { c.ErrorRateWeight = -1 }, func(c *PrioritySchedulingConfig) { c.ErrorRateWeight = 101 }, func(c *PrioritySchedulingConfig) { c.ErrorRateWeight = math.NaN() }, func(c *PrioritySchedulingConfig) { c.ErrorRateWeight = math.Inf(1) },
		func(c *PrioritySchedulingConfig) { c.ErrorRateWindowMinutes = 0 }, func(c *PrioritySchedulingConfig) { c.ErrorRateWindowMinutes = 61 }, func(c *PrioritySchedulingConfig) { c.ErrorRatePriorSamples = 0 }, func(c *PrioritySchedulingConfig) { c.ErrorRatePriorSamples = 1001 },
	} {
		c := DefaultPrioritySchedulingConfig()
		change(&c)
		require.Error(t, ValidatePrioritySchedulingConfig(c))
	}
	c := DefaultPrioritySchedulingConfig()
	c.ErrorRateWeight = 0
	require.NoError(t, ValidatePrioritySchedulingConfig(c))
}
