package task

import (
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/utils"
)

func polygonInit() {
	pol := evm{
		Network: conf.Polygon,
		Block: block{
			RollDelayOffset: 2,
			ConfirmedOffset: 40,
		},
		Client: utils.NewHttpClient(),
	}

	Register(Task{Callback: pol.scanBlocks, Duration: time.Second * 5})
	Register(Task{Callback: pol.tradeConfirmHandle, Duration: time.Second * 5})

}
