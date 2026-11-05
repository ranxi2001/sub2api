package task

import (
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/utils"
)

func plasmaInit() {
	xpl := evm{
		Network: conf.Plasma,
		Block: block{
			RollDelayOffset: 2,
			ConfirmedOffset: 40,
		},
		Client: utils.NewHttpClient(),
	}

	Register(Task{Callback: xpl.scanBlocks, Duration: time.Second * 5})
	Register(Task{Callback: xpl.tradeConfirmHandle, Duration: time.Second * 5})

}
