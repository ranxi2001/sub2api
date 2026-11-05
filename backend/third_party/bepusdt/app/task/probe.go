package task

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/utils"
	"github.com/v03413/tronprotocol/api"
)

// ProbeNetwork checks the methods the scanner and confirmation workers need.
// It never creates an order or credits an account.
func ProbeNetwork(parent context.Context, network string) error {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	if _, ok := conf.EVMNetworks[network]; ok {
		e := evm{Network: network, Client: utils.NewHttpClient()}
		defer e.Client.CloseIdleConnections()
		head, err := e.rpc(ctx, "eth_blockNumber", []any{})
		if err != nil {
			return err
		}
		number, _ := hexInt(head.String())
		if number < 32 {
			return errors.New("invalid chain height")
		}
		// Public RPC clusters can advertise a tip before every backend has it.
		// Two blocks of slack still detect stale nodes without rejecting a live chain.
		latest, err := e.blockHeader(ctx, number-2)
		if err != nil {
			return err
		}
		stamp, _ := hexInt(latest.Get("timestamp").String())
		if stamp < time.Now().Add(-5*time.Minute).Unix() || stamp > time.Now().Add(2*time.Minute).Unix() {
			return errors.New("RPC node is not synchronized")
		}
		block, err := e.blockHeader(ctx, number-30)
		if err != nil {
			return err
		}
		_, err = e.rpc(ctx, "eth_getLogs", []any{map[string]any{"fromBlock": fmt.Sprintf("0x%x", number-30), "toBlock": fmt.Sprintf("0x%x", number-21), "address": evmContracts(network), "topics": []string{evmTransferEvent}}})
		if err != nil {
			return err
		}
		tx := "0x" + strings.Repeat("0", 64)
		if first := block.Get("transactions.0").String(); evmHex(first, 32) {
			tx = first
		}
		_, err = e.rpc(ctx, "eth_getTransactionReceipt", []any{tx})
		return err
	}
	switch network {
	case conf.Tron:
		conn, err := tr.client()
		if err != nil {
			return errors.New("Tron node connection failed")
		}
		client := api.NewWalletClient(conn)
		head, err := client.GetNowBlock2(ctx, nil)
		if err != nil {
			return errors.New("Tron head request failed")
		}
		height := head.GetBlockHeader().GetRawData().GetNumber()
		if height <= 0 {
			return errors.New("invalid Tron head")
		}
		if !recentNodeTime(head.GetBlockHeader().GetRawData().GetTimestamp() / 1000) {
			return errors.New("Tron node is not synchronized")
		}
		block, err := client.GetBlockByNum2(ctx, &api.NumberMessage{Num: height - 30})
		if err != nil {
			return errors.New("Tron block request failed")
		}
		if block.GetBlockHeader().GetRawData().GetNumber() != height-30 {
			return errors.New("invalid Tron block")
		}
		if len(block.GetTransactions()) > 0 {
			_, err = client.GetTransactionInfoById(ctx, &api.BytesMessage{Value: block.GetTransactions()[0].GetTxid()})
			if err != nil {
				return errors.New("Tron receipt request failed")
			}
		}
		return nil
	case conf.Solana:
		head, err := nodeJSON(ctx, sol.client, http.MethodPost, model.Endpoint(conf.Solana), rpcRequest(1, "getSlot", []any{}))
		if err != nil {
			return err
		}
		slot := head.Get("result").Int()
		if slot <= 32 {
			return errors.New("invalid Solana slot")
		}
		blocks, err := nodeJSON(ctx, sol.client, http.MethodPost, model.Endpoint(conf.Solana), rpcRequest(1, "getBlocks", []any{slot - 32, slot - 1}))
		if err != nil {
			return err
		}
		items := blocks.Get("result").Array()
		if len(items) == 0 {
			return errors.New("Solana blocks unavailable")
		}
		body, err := nodeJSON(ctx, sol.client, http.MethodPost, model.Endpoint(conf.Solana), rpcRequest(1, "getBlock", []any{items[len(items)-1].Int(), map[string]any{"encoding": "json", "maxSupportedTransactionVersion": 1, "transactionDetails": "full", "rewards": false}}))
		if err != nil {
			return err
		}
		if body.Get("result.blockTime").Int() <= 0 {
			return errors.New("Solana block unavailable")
		}
		if !recentNodeTime(body.Get("result.blockTime").Int()) {
			return errors.New("Solana node is not synchronized")
		}
		if signature := body.Get("result.transactions.0.transaction.signatures.0").String(); signature != "" {
			_, err = nodeJSON(ctx, sol.client, http.MethodPost, model.Endpoint(conf.Solana), rpcRequest(1, "getSignatureStatuses", []any{[]string{signature}, map[string]any{"searchTransactionHistory": true}}))
		}
		return err
	case conf.Aptos:
		base := strings.TrimRight(model.Endpoint(conf.Aptos), "/") + "/v1"
		head, err := nodeJSON(ctx, apt.client, http.MethodGet, base, nil)
		if err != nil {
			return err
		}
		version := head.Get("ledger_version").Int()
		if version < 2 {
			return errors.New("invalid Aptos version")
		}
		if !recentNodeTime(head.Get("ledger_timestamp").Int() / 1000000) {
			return errors.New("Aptos node is not synchronized")
		}
		block, err := nodeJSON(ctx, apt.client, http.MethodGet, fmt.Sprintf("%s/blocks/by_version/%d?with_transactions=false", base, version-1), nil)
		if err != nil {
			return err
		}
		if block.Get("block_timestamp").Int() <= 0 {
			return errors.New("Aptos block time unavailable")
		}
		body, err := nodeJSON(ctx, apt.client, http.MethodGet, fmt.Sprintf("%s/transactions?start=%d&limit=1", base, version-1), nil)
		if err != nil {
			return err
		}
		items := body.Array()
		if len(items) != 1 {
			return errors.New("Aptos transactions unavailable")
		}
		hash := items[0].Get("hash").String()
		if hash == "" {
			return errors.New("invalid Aptos transaction")
		}
		_, err = nodeJSON(ctx, apt.client, http.MethodGet, base+"/transactions/by_hash/"+hash, nil)
		return err
	case conf.Ton:
		client, err := tn.client()
		if err != nil {
			return err
		}
		head, err := client.CurrentMasterchainInfo(ctx)
		if err != nil {
			return errors.New("TON head request failed")
		}
		block, err := client.GetBlockData(ctx, head)
		if err != nil {
			return errors.New("TON block request failed")
		}
		if block.GlobalID != -239 {
			return errors.New("different TON network")
		}
		if !recentNodeTime(int64(block.BlockInfo.GenUtime)) {
			return errors.New("TON node is not synchronized")
		}
		_, err = client.GetBlockShardsInfo(ctx, head)
		if err != nil {
			return errors.New("TON shard request failed")
		}
		return nil
	}
	return errors.New("unsupported collection network")
}

func recentNodeTime(stamp int64) bool {
	now := time.Now()
	return stamp >= now.Add(-5*time.Minute).Unix() && stamp <= now.Add(2*time.Minute).Unix()
}
