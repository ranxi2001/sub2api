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
	"github.com/v03413/tronprotocol/api"
)

func (t *tron) lookbackRange(parent context.Context, start, end int64) (int64, int64, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	conn, err := t.client()
	if err != nil {
		return 0, 0, err
	}
	client := api.NewWalletClient(conn)
	head, err := client.GetNowBlock2(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	return timeRange(ctx, start, end, head.GetBlockHeader().GetRawData().GetNumber(), func(ctx context.Context, height int64) (int64, error) {
		block, err := client.GetBlockByNum2(ctx, &api.NumberMessage{Num: height})
		if err != nil {
			return 0, err
		}
		raw := block.GetBlockHeader().GetRawData()
		if raw.GetNumber() != height || raw.GetTimestamp() <= 0 {
			return 0, errors.New("invalid Tron block")
		}
		return raw.GetTimestamp() / 1000, nil
	})
}

func (s *solana) lookbackRange(parent context.Context, start, end int64) (int64, int64, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	head, err := nodeJSON(ctx, s.client, http.MethodPost, model.Endpoint(conf.Solana), rpcRequest(1, "getSlot", []any{}))
	if err != nil {
		return 0, 0, err
	}
	return timeRange(ctx, start, end, head.Get("result").Int(), func(ctx context.Context, height int64) (int64, error) {
		value, err := nodeJSON(ctx, s.client, http.MethodPost, model.Endpoint(conf.Solana), rpcRequest(1, "getBlockTime", []any{height}))
		if err == nil && value.Get("result").Int() > 0 {
			return value.Get("result").Int(), nil
		}
		blocks, err := nodeJSON(ctx, s.client, http.MethodPost, model.Endpoint(conf.Solana), rpcRequest(1, "getBlocks", []any{max(0, height-128), height}))
		if err != nil {
			return 0, err
		}
		slots := blocks.Get("result").Array()
		if len(slots) == 0 {
			return 0, errors.New("no produced Solana slot in range")
		}
		value, err = nodeJSON(ctx, s.client, http.MethodPost, model.Endpoint(conf.Solana), rpcRequest(1, "getBlockTime", []any{slots[len(slots)-1].Int()}))
		if err != nil || value.Get("result").Int() <= 0 {
			return 0, errors.New("Solana block time unavailable")
		}
		return value.Get("result").Int(), nil
	})
}

func (a *aptos) lookbackRange(parent context.Context, start, end int64) (int64, int64, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	base := strings.TrimRight(model.Endpoint(conf.Aptos), "/") + "/v1"
	head, err := nodeJSON(ctx, a.client, http.MethodGet, base, nil)
	if err != nil {
		return 0, 0, err
	}
	return timeRange(ctx, start, end, head.Get("ledger_version").Int(), func(ctx context.Context, height int64) (int64, error) {
		// Checkpoint transactions have no timestamp. The containing block does.
		value, err := nodeJSON(ctx, a.client, http.MethodGet, fmt.Sprintf("%s/blocks/by_version/%d?with_transactions=false", base, height), nil)
		if err != nil {
			return 0, err
		}
		if height == 0 {
			return 0, nil
		}
		stamp := value.Get("block_timestamp").Int() / 1000000
		if stamp <= 0 {
			return 0, errors.New("Aptos version time unavailable")
		}
		return stamp, nil
	})
}

func (t *ton) lookbackRange(parent context.Context, start, end int64) (int64, int64, error) {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	client, err := t.client()
	if err != nil {
		return 0, 0, err
	}
	head, err := client.CurrentMasterchainInfo(ctx)
	if err != nil {
		return 0, 0, err
	}
	return timeRange(ctx, start, end, int64(head.SeqNo), func(ctx context.Context, height int64) (int64, error) {
		block, err := client.LookupBlock(ctx, tonMasterChainID, tonMasterShard, uint32(height))
		if err != nil {
			return 0, err
		}
		data, err := client.GetBlockData(ctx, block)
		if err != nil {
			return 0, err
		}
		if data.GlobalID != -239 {
			return 0, errors.New("different TON network")
		}
		return int64(data.BlockInfo.GenUtime), nil
	})
}
