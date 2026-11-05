package task

import (
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/utils"
)

func baseInit() {
	base := evm{
		Network: conf.Base,
		Block: block{
			RollDelayOffset: 2,
			ConfirmedOffset: 40,
		},
		Client: utils.NewHttpClient(),
	}

	Register(Task{Callback: base.scanBlocks, Duration: time.Second * 5})
	Register(Task{Callback: base.tradeConfirmHandle, Duration: time.Second * 5})

}
