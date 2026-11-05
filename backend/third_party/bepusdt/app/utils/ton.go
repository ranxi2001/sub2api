package utils

import (
	"context"
	"strings"
	"time"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	tgo "github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/ton/jetton"
)

func IsValidTonAddress(value string) bool {
	if !strings.HasPrefix(value, "UQ") {
		return false
	}

	_, err := address.ParseAddr(value)

	return err == nil
}

func NewTonClient(configUrl string) (tgo.APIClientWrapped, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := liteclient.NewConnectionPool()
	if err := pool.AddConnectionsFromConfigUrl(ctx, configUrl); err != nil {
		pool.Stop()
		return nil, err
	}
	return tgo.NewAPIClient(pool).WithRetryTimeout(3, time.Second*5), nil
}

func GetJettonWalletAddr(api tgo.APIClientWrapped, master *address.Address, owner *address.Address) (*address.Address, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	client := jetton.NewJettonMasterClient(api, master)
	wallet, err := client.GetJettonWallet(ctx, owner)
	if err != nil {
		return nil, err
	}

	return wallet.Address(), err
}
