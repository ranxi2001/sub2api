package payment

// BEpusdtTradeTypes follows BEpusdt's docs/trade-type.md. The values are both
// the site's payment method keys and the upstream trade_type values.
func BEpusdtTradeTypes() []PaymentType {
	return []PaymentType{
		"usdt.trc20", "usdt.erc20", "usdt.bep20", "usdt.polygon",
		"usdt.aptos", "usdt.solana", "usdt.xlayer", "usdt.arbitrum", "usdt.plasma", "usdt.ton",
		"usdc.trc20", "usdc.erc20", "usdc.bep20", "usdc.polygon",
		"usdc.aptos", "usdc.solana", "usdc.xlayer", "usdc.arbitrum", "usdc.base",
		"tron.trx", "ethereum.eth", "bsc.bnb", "ton.gram",
	}
}

func IsBEpusdtTradeType(method string) bool {
	for _, supported := range BEpusdtTradeTypes() {
		if method == supported {
			return true
		}
	}
	return false
}
