package task

import (
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/utils"
)

func ethInit() {
	eth := evm{
		Network: conf.Ethereum,
		Block: block{
			RollDelayOffset: 2,
			ConfirmedOffset: 12,
		},
		Native: evmNative{
			Parse:     true,
			TradeType: model.EthereumEth,
			Decimal:   conf.EthereumEthDecimals,
		},
		Client: utils.NewHttpClient(),
	}

	Register(Task{Callback: eth.scanBlocks, Duration: time.Second * 12})
	Register(Task{Callback: eth.tradeConfirmHandle, Duration: time.Second * 5})

}
