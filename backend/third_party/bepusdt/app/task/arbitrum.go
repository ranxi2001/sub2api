package task

import (
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/utils"
)

func arbitrumInit() {
	arb := evm{
		Network: conf.Arbitrum,
		Block: block{
			RollDelayOffset: 2,
			ConfirmedOffset: 40,
		},
		Client: utils.NewHttpClient(),
	}

	Register(Task{Callback: arb.scanBlocks, Duration: time.Second * 5})
	Register(Task{Callback: arb.tradeConfirmHandle, Duration: time.Second * 5})

}
