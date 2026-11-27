package usagestats

// AccountStats 账号使用统计
//
// cost: 账号口径费用（使用 total_cost * account_rate_multiplier）
// standard_cost: 标准费用（使用 total_cost，不含倍率）
// user_cost: 用户/API Key 口径费用（使用 actual_cost，受分组倍率影响）
type AccountStats struct {
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	Cost         float64 `json:"cost"`
	StandardCost float64 `json:"standard_cost"`
	UserCost     float64 `json:"user_cost"`
}

// ProcurementCoverage sums only priced requests in the selected reporting window.
type ProcurementCoverage struct {
	Amount   *float64 `json:"amount"`
	Known    int64    `json:"known"`
	Requests int64    `json:"requests"`
}

type AccountProcurementWindows struct {
	Window   ProcurementCoverage
	Lifetime ProcurementCoverage
}
