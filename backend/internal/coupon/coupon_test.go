package coupon

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCouponCalculations(t *testing.T) {
	base := Template{Name: "test", Kind: "fixed", Currency: "CNY", MinAmount: 100, Value: 10, ValidDays: 7, Enabled: true}
	tests := []struct {
		name, kind                                       string
		value, cap, amount, pay, credit, discount, bonus float64
		currency                                         string
		fail                                             bool
	}{
		{"fixed", "fixed", 10, 0, 100, 100, 200, 10, 0, "CNY", false},
		{"percent cap", "percent", 10, 5, 100, 100, 200, 5, 0, "CNY", false},
		{"tier stacked percent", "percent", 10, 0, 100, 90, 200, 9, 0, "CNY", false},
		{"bonus", "bonus", 10, 0, 100, 100, 200, 0, 20, "CNY", false},
		{"threshold", "fixed", 10, 0, 99, 99, 198, 0, 0, "CNY", true},
		{"currency", "fixed", 10, 0, 100, 100, 200, 0, 0, "USD", true},
		{"zero payment", "fixed", 10, 0, 100, 10, 200, 0, 0, "CNY", true},
		{"100 percent", "percent", 100, 0, 100, 100, 200, 0, 0, "CNY", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base
			c.Kind = tt.kind
			c.Value = tt.value
			c.MaxDiscount = tt.cap
			d, b, err := Calculate(c, tt.amount, tt.pay, tt.credit, tt.currency, 2)
			if tt.fail {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.discount, d)
			require.Equal(t, tt.bonus, b)
		})
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), -1, 0, 1.001} {
		c := base
		c.Value = v
		require.Error(t, c.Validate())
	}
}
