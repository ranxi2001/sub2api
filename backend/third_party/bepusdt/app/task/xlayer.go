package task

import (
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/utils"
)

func xlayerInit() {
	xlayer := evm{
		Network: conf.Xlayer,
		Block: block{
			RollDelayOffset: 3,
			ConfirmedOffset: 12,
		},
		Client: utils.NewHttpClient(),
	}

	Register(Task{Callback: xlayer.scanBlocks, Duration: time.Second * 3})
	Register(Task{Callback: xlayer.tradeConfirmHandle, Duration: time.Second * 5})

}
