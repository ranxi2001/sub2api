package conf

// Public read-only fallbacks. Each EVM endpoint is checked against ChainID
// before it can supply data to the collection scanner.
type EVMNetwork struct {
	ChainID int64
	URLs    []string
}

var EVMNetworks = map[string]EVMNetwork{
	Bsc:      {56, []string{"https://bsc-rpc.publicnode.com", "https://bsc-dataseed.bnbchain.org", "https://bsc.drpc.org", "https://1rpc.io/bnb"}},
	Ethereum: {1, []string{"https://ethereum-rpc.publicnode.com", "https://eth.drpc.org", "https://1rpc.io/eth"}},
	Polygon:  {137, []string{"https://polygon-bor-rpc.publicnode.com", "https://polygon.drpc.org", "https://1rpc.io/matic"}},
	Arbitrum: {42161, []string{"https://arb1.arbitrum.io/rpc", "https://arbitrum-one-rpc.publicnode.com"}},
	Base:     {8453, []string{"https://1rpc.io/base", "https://base.drpc.org", "https://base-rpc.publicnode.com", "https://mainnet.base.org"}},
	Xlayer:   {196, []string{"https://xlayerrpc.okx.com", "https://rpc.xlayer.tech"}},
	Plasma:   {9745, []string{"https://rpc.plasma.to"}},
}
