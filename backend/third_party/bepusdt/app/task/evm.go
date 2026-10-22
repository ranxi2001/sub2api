package task

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
)

const (
	blockParseMaxNum = 10
	evmTransferEvent = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
)

var chainBlockNum sync.Map

type block struct {
	RollDelayOffset int64
	ConfirmedOffset int
}

type evmNative struct {
	Parse     bool
	Decimal   int32
	TradeType model.TradeType
}

type evm struct {
	Network string
	Block   block
	Native  evmNative
	Client  *http.Client
	// Only scanBlocks owns these cursors. A failed range never advances them.
	nextBlock  int64
	startAt    int64
	seenOrders map[int64]bool
}

type evmBlock struct{ From, To int64 }

type evmInterest struct {
	addresses map[string]bool
	native    bool
	watchAll  bool
	startAt   int64
	scan      bool
	active    bool
	orderIDs  map[int64]bool
	newAt     int64
}

func (e *evm) interest() (evmInterest, error) {
	interest := evmInterest{addresses: make(map[string]bool), orderIDs: make(map[int64]bool)}
	trades := model.GetNetworkTrades(model.Network(e.Network))
	var orders []model.Order
	if err := model.Db.Where("status IN ? AND trade_type IN ? AND expired_at > ?", receivableOrderStatuses(), trades, time.Now().Add(model.GetLookbackHour())).Find(&orders).Error; err != nil {
		return interest, err
	}
	for _, order := range orders {
		interest.active = true
		interest.orderIDs[order.ID] = true
		if order.Status == model.OrderStatusConfirming {
			continue
		}
		interest.scan = true
		created := order.CreatedAt.Time().Unix() - 1
		if !e.seenOrders[order.ID] && (interest.newAt == 0 || created < interest.newAt) {
			interest.newAt = created
		}
		if interest.startAt == 0 || created < interest.startAt {
			interest.startAt = created
		}
		interest.addresses[strings.ToLower(orderMatchAddress(order))] = true
		interest.native = interest.native || order.TradeType == e.Native.TradeType
	}
	var watched int64
	if err := model.Db.Model(&model.Wallet{}).Where("other_notify = ? AND trade_type IN ?", model.WaOtherEnable, trades).Count(&watched).Error; err != nil {
		return interest, err
	}
	interest.watchAll = watched > 0 || mqttSubscribed(e.Network)
	if interest.watchAll {
		interest.active, interest.scan, interest.native = true, true, e.Native.Parse
	}
	return interest, nil
}

// One bounded cursor handles both new blocks and missed transfers. Startup
// locates the oldest unpaid order directly on-chain, without a block-time API.
// Restarts replay eligible orders; outages never skip ahead to the new head.
func (e *evm) scanBlocks(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	interest, err := e.interest()
	if err != nil {
		e.scanFailure(err)
		return
	}
	if !interest.active {
		e.nextBlock, e.startAt, e.seenOrders = 0, 0, nil
		return
	}
	result, err := e.rpc(ctx, "eth_blockNumber", []any{})
	if err != nil {
		e.scanFailure(err)
		return
	}
	head, _ := hexInt(result.String())
	chainBlockNum.Store(e.Network, head)
	conf.RecordHead(e.Network, fmt.Sprint(head))
	target := head - e.Block.RollDelayOffset
	if !interest.scan || target <= 0 {
		return
	}
	if e.nextBlock == 0 || (interest.startAt > 0 && interest.startAt < e.startAt) {
		start := target
		if interest.startAt > 0 {
			start, err = e.blockAtTime(ctx, interest.startAt, target)
			if err != nil {
				e.scanFailure(err)
				return
			}
		}
		e.nextBlock, e.startAt = start, interest.startAt
	} else if interest.newAt > 0 {
		// An order can be created while the preceding poll is querying logs.
		// Replay its creation block before adding its address to the scan set.
		start, err := e.blockAtTime(ctx, interest.newAt, target)
		if err != nil {
			e.scanFailure(err)
			return
		}
		e.nextBlock = min(e.nextBlock, start)
	}
	e.seenOrders = interest.orderIDs
	for batches := 0; e.nextBlock <= target && batches < 32; batches++ {
		to := min(e.nextBlock+blockParseMaxNum-1, target)
		if err := e.scanRange(ctx, evmBlock{e.nextBlock, to}, interest); err != nil {
			e.scanFailure(err)
			return
		}
		e.nextBlock = to + 1
		conf.RecordSuccess(e.Network, fmt.Sprint(to))
	}
}

func (e *evm) scanFailure(err error) {
	conf.RecordError(e.Network, err.Error())
	log.Task.Warn(fmt.Sprintf("EVM scanner (%s), retrying without advancing cursor: %v", e.Network, err))
}

func (e *evm) blockHeader(ctx context.Context, height int64) (gjson.Result, error) {
	header, err := e.rpc(ctx, "eth_getBlockByNumber", []any{fmt.Sprintf("0x%x", height), false})
	if err != nil {
		return header, err
	}
	number, nerr := hexInt(header.Get("number").String())
	timestamp, terr := hexInt(header.Get("timestamp").String())
	if nerr != nil || terr != nil || number != height || timestamp < 0 || !evmHex(header.Get("hash").String(), 32) {
		return header, errors.New("invalid RPC block header")
	}
	return header, nil
}

// Search near the head first so public nodes do not need archive-state access.
// Return a block at or just before the order's creation time.
func (e *evm) blockAtTime(ctx context.Context, at, head int64) (int64, error) {
	return heightAtTime(ctx, at, head, func(ctx context.Context, height int64) (int64, error) {
		header, err := e.blockHeader(ctx, height)
		if err != nil {
			return 0, err
		}
		return hexInt(header.Get("timestamp").String())
	})
}

func evmContracts(network string) []string {
	var contracts []string
	for _, config := range model.GetAllTradeConfig() {
		if string(config.Network) == network && config.Contract != "" {
			contracts = append(contracts, config.Contract)
		}
	}
	sort.Strings(contracts)
	return contracts
}

func (e *evm) scanRange(ctx context.Context, b evmBlock, interest evmInterest) error {
	var transfers []transfer
	logHashes := make(map[int64]string)
	blockNumbers := make(map[int64]bool)
	contracts := evmContracts(e.Network)
	if len(contracts) > 0 {
		topics := []any{evmTransferEvent}
		if !interest.watchAll {
			var recipients []string
			for address := range interest.addresses {
				if !evmHex(address, 20) {
					return errors.New("invalid EVM receiving address")
				}
				recipients = append(recipients, "0x"+strings.Repeat("0", 24)+address[2:])
			}
			sort.Strings(recipients)
			if len(recipients) == 0 {
				return errors.New("no receiving addresses to scan")
			}
			topics = append(topics, nil, recipients)
		}
		filter := map[string]any{"fromBlock": fmt.Sprintf("0x%x", b.From), "toBlock": fmt.Sprintf("0x%x", b.To), "address": contracts, "topics": topics}
		logs, err := e.rpc(ctx, "eth_getLogs", []any{filter})
		if err != nil {
			return err
		}
		for _, item := range logs.Array() {
			if item.Get("removed").Bool() {
				continue
			}
			contract := strings.ToLower(item.Get("address").String())
			trade, ok := model.GetContractTrade(contract)
			if !ok || string(model.GetAllTradeConfig()[string(trade)].Network) != e.Network {
				continue
			}
			event := item.Get("topics").Array()
			if len(event) != 3 || event[0].String() != evmTransferEvent {
				continue
			}
			if !evmHex(event[1].String(), 32) || !evmHex(event[2].String(), 32) || !evmHex(item.Get("data").String(), 32) {
				return errors.New("malformed token transfer log")
			}
			recipient := "0x" + strings.ToLower(event[2].String()[26:])
			if !interest.watchAll && !interest.addresses[recipient] {
				continue
			}
			amount, _ := new(big.Int).SetString(item.Get("data").String()[2:], 16)
			if amount.Sign() <= 0 {
				continue
			}
			height, err := hexInt(item.Get("blockNumber").String())
			if err != nil || height < b.From || height > b.To || !evmHex(item.Get("transactionHash").String(), 32) || !evmHex(item.Get("blockHash").String(), 32) {
				return errors.New("token log has invalid block or transaction identity")
			}
			blockNumbers[height] = true
			logHashes[height] = item.Get("blockHash").String()
			transfers = append(transfers, transfer{Network: e.Network, TradeType: trade, RecvAddress: recipient,
				FromAddress: "0x" + strings.ToLower(event[1].String()[26:]), Amount: decimal.NewFromBigInt(amount, model.GetContractDecimal(contract)),
				TxHash: strings.ToLower(item.Get("transactionHash").String()), BlockNum: int(height)})
		}
	}
	if interest.native {
		for height := b.From; height <= b.To; height++ {
			blockNumbers[height] = true
		}
	}
	var calls []evmRPCRequest
	for height := range blockNumbers {
		calls = append(calls, rpcRequest(height, "eth_getBlockByNumber", []any{fmt.Sprintf("0x%x", height), interest.native}))
	}
	responses, err := e.rpcBatch(ctx, calls, true)
	if err != nil {
		return err
	}
	times := make(map[int64]time.Time)
	for _, response := range responses {
		header := response.Get("result")
		height, nerr := hexInt(header.Get("number").String())
		stamp, terr := hexInt(header.Get("timestamp").String())
		if nerr != nil || terr != nil || stamp <= 0 || height != response.Get("id").Int() || !blockNumbers[height] {
			return errors.New("RPC returned a different or incomplete block")
		}
		if hash, ok := logHashes[height]; ok && header.Get("hash").String() != hash {
			return errors.New("transfer log is not in the canonical block")
		}
		times[height] = time.Unix(stamp, 0)
		if interest.native {
			transfers = append(transfers, e.parseNativeTransfer(header.Get("transactions").Array(), int(height), times[height])...)
		}
	}
	for i := range transfers {
		transfers[i].Timestamp = times[int64(transfers[i].BlockNum)]
	}
	if len(transfers) > 0 {
		select {
		case transferQueue.In <- transfers:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (e *evm) parseNativeTransfer(items []gjson.Result, height int, at time.Time) []transfer {
	var transfers []transfer
	for _, tx := range items {
		if tx.Get("input").String() != "0x" || !evmHex(tx.Get("to").String(), 20) || !evmHex(tx.Get("hash").String(), 32) {
			continue
		}
		value := tx.Get("value").String()
		if !strings.HasPrefix(value, "0x") {
			continue
		}
		amount, ok := new(big.Int).SetString(value[2:], 16)
		if !ok || amount.Sign() <= 0 {
			continue
		}
		transfers = append(transfers, transfer{Network: e.Network, FromAddress: strings.ToLower(tx.Get("from").String()), RecvAddress: strings.ToLower(tx.Get("to").String()),
			Amount: decimal.NewFromBigInt(amount, e.Native.Decimal), TxHash: strings.ToLower(tx.Get("hash").String()), BlockNum: height, Timestamp: at, TradeType: e.Native.TradeType})
	}
	return transfers
}

func (e *evm) tradeConfirmHandle(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	last, ok := chainBlockNum.Load(e.Network)
	if !ok {
		return
	}
	for _, order := range getConfirmingOrders(model.GetNetworkTrades(model.Network(e.Network))) {
		if last.(int64)-int64(order.RefBlockNum) < int64(e.Block.ConfirmedOffset) {
			continue
		}
		if err := e.confirmOrder(ctx, order); err != nil {
			e.scanFailure(err)
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func (e *evm) confirmOrder(ctx context.Context, order model.Order) error {
	receipt, err := e.rpc(ctx, "eth_getTransactionReceipt", []any{order.RefHash})
	if err != nil {
		return err
	}
	if receipt.Raw == "null" {
		return nil
	}
	height, err := hexInt(receipt.Get("blockNumber").String())
	if err != nil || height != int64(order.RefBlockNum) || !strings.EqualFold(receipt.Get("transactionHash").String(), order.RefHash) || receipt.Get("status").String() != "0x1" {
		return errors.New("transaction receipt does not match the detected payment")
	}
	header, err := e.blockHeader(ctx, height)
	if err != nil {
		return err
	}
	stamp, _ := hexInt(header.Get("timestamp").String())
	at := time.Unix(stamp, 0)
	if header.Get("hash").String() != receipt.Get("blockHash").String() || !order.CreatedAt.Before(at) || !order.ExpiredAt.After(at) {
		return errors.New("payment is outside its order window or canonical block")
	}
	config := model.GetAllTradeConfig()[string(order.TradeType)]
	if config.Contract != "" && !receiptHasPayment(receipt, order, config.Contract, config.Decimal) {
		return errors.New("receipt does not contain the expected token transfer")
	}
	markFinalConfirmed(order)
	return nil
}

func receiptHasPayment(receipt gjson.Result, order model.Order, contract string, decimals int32) bool {
	for _, item := range receipt.Get("logs").Array() {
		topics := item.Get("topics").Array()
		if item.Get("removed").Bool() || !strings.EqualFold(item.Get("address").String(), contract) || len(topics) != 3 || topics[0].String() != evmTransferEvent {
			continue
		}
		if !evmHex(topics[2].String(), 32) || !evmHex(item.Get("data").String(), 32) {
			continue
		}
		amount, _ := new(big.Int).SetString(item.Get("data").String()[2:], 16)
		if strings.EqualFold("0x"+topics[2].String()[26:], orderMatchAddress(order)) && amountMatch(decimal.NewFromBigInt(amount, decimals), order.Amount, string(order.TradeType)) {
			return true
		}
	}
	return false
}

func evmHex(value string, size int) bool {
	if !strings.HasPrefix(value, "0x") || len(value) != 2+size*2 {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}

func (e *evm) rpcEndpoint() string { return model.Endpoint(model.Network(e.Network)) }

func syncBreak(network string, num int) bool {
	if num >= blockQueueLimit {
		return true
	}
	if mqttSubscribed(network) {
		return false
	}
	trades := model.GetNetworkTrades(model.Network(network))
	if len(trades) == 0 {
		return true
	}
	var count int64
	model.Db.Model(&model.Wallet{}).Where("other_notify = ? AND trade_type IN ?", model.WaOtherEnable, trades).Count(&count)
	return count == 0 && !hasLookbackOrders(trades)
}
