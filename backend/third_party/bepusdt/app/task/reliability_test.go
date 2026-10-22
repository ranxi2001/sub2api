package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"github.com/v03413/bepusdt/app/conf"
	belog "github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func testCollectionDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "collection.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	model.Db = db
	require.NoError(t, model.AutoMigrate())
	model.FillDefaultConf()
	model.RefreshC()
	require.NoError(t, belog.Init(filepath.Join(dir, "logs")))
	lookbackDone = sync.Map{}
	lookbackEpochs = sync.Map{}
	replayCursors.items = make(map[model.Network]*replayCursor)
	t.Cleanup(func() { model.Close(); belog.Close() })
}

func testSetting(t *testing.T, key model.ConfKey, value string) {
	t.Helper()
	require.NoError(t, model.Db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "k"}}, DoUpdates: clause.AssignmentColumns([]string{"v"})}).Create(&model.Conf{K: key, V: value}).Error)
	model.RefreshC()
}

func testOrder(t *testing.T, trade model.TradeType, address string, created, expiry time.Time) model.Order {
	t.Helper()
	createdAt := model.Datetime(created)
	epoch := time.Unix(0, 0)
	order := model.Order{OrderId: fmt.Sprintf("test-%d", time.Now().UnixNano()), TradeId: fmt.Sprintf("trade-%d", time.Now().UnixNano()), TradeType: trade, Fiat: "USD", Crypto: "USDT", Rate: "1", Money: "1.49", Amount: "1.49", Address: address, MatchAddress: address, Status: model.OrderStatusWaiting, ConfirmedAt: &epoch, ExpiredAt: expiry, AutoTimeAt: model.AutoTimeAt{CreatedAt: &createdAt}}
	require.NoError(t, model.Db.Create(&order).Error)
	return order
}

type fakeEVM struct {
	chain        int64
	head         atomic.Int64
	baseTime     int64
	blockSeconds int64
	missingTip   bool
	failLogs     atomic.Bool
	mu           sync.Mutex
	ranges       [][2]int64
	filters      []map[string]any
}

func (f *fakeEVM) serve(w http.ResponseWriter, r *http.Request) {
	var call struct {
		ID     int64             `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if json.NewDecoder(r.Body).Decode(&call) != nil {
		http.Error(w, "bad request", 400)
		return
	}
	var result any
	switch call.Method {
	case "eth_chainId":
		result = fmt.Sprintf("0x%x", f.chain)
	case "eth_blockNumber":
		result = fmt.Sprintf("0x%x", f.head.Load())
	case "eth_getBlockByNumber":
		var number string
		_ = json.Unmarshal(call.Params[0], &number)
		height, _ := hexInt(number)
		seconds := max(f.blockSeconds, 1)
		result = map[string]any{"number": number, "timestamp": fmt.Sprintf("0x%x", f.baseTime+height*seconds), "hash": fmt.Sprintf("0x%064x", height), "transactions": []any{}}
		if f.missingTip && height == f.head.Load() {
			result = nil
		}
	case "eth_getTransactionReceipt":
		result = nil
	case "eth_getLogs":
		if f.failLogs.Load() {
			http.Error(w, "subscription required", 403)
			return
		}
		var filter map[string]any
		_ = json.Unmarshal(call.Params[0], &filter)
		from, _ := hexInt(filter["fromBlock"].(string))
		to, _ := hexInt(filter["toBlock"].(string))
		f.mu.Lock()
		f.ranges = append(f.ranges, [2]int64{from, to})
		f.filters = append(f.filters, filter)
		f.mu.Unlock()
		if filter["address"] == nil {
			http.Error(w, "contract filter required", 400)
			return
		}
		result = []any{}
	default:
		http.Error(w, "unexpected method", 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": result})
}

func noPublicFallbacks(t *testing.T, network string) {
	t.Helper()
	old := conf.EVMNetworks[network]
	next := old
	next.URLs = nil
	conf.EVMNetworks[network] = next
	t.Cleanup(func() { conf.EVMNetworks[network] = old })
}

func TestEVMBackfillRetriesSameRangeAfterEndpointChange(t *testing.T) {
	testCollectionDB(t)
	noPublicFallbacks(t, conf.Bsc)
	base := time.Now().Add(-200 * time.Second).Unix()
	order := testOrder(t, model.UsdtBep20, "0x"+strings.Repeat("1", 40), time.Unix(base+50, 0), time.Now().Add(time.Hour))
	bad := &fakeEVM{chain: 56, baseTime: base}
	bad.head.Store(120)
	bad.failLogs.Store(true)
	server := httptest.NewServer(http.HandlerFunc(bad.serve))
	defer server.Close()
	testSetting(t, model.RpcEndpointBsc, server.URL)
	e := evm{Network: conf.Bsc, Client: server.Client(), nextBlock: 100, startAt: base + 49, seenOrders: map[int64]bool{order.ID: true}}
	e.scanBlocks(context.Background())
	require.Equal(t, int64(100), e.nextBlock)
	require.Equal(t, "error", conf.GetStats()[conf.Bsc].Status)
	good := &fakeEVM{chain: 56, baseTime: base}
	good.head.Store(5000)
	replacement := httptest.NewServer(http.HandlerFunc(good.serve))
	defer replacement.Close()
	testSetting(t, model.RpcEndpointBsc, replacement.URL)
	e.scanBlocks(context.Background())
	require.Equal(t, [2]int64{100, 109}, good.ranges[0], "outage recovery must not jump to the latest head")
	require.Equal(t, int64(420), e.nextBlock, "work per poll is bounded")
	require.Equal(t, "healthy", conf.GetStats()[conf.Bsc].Status)
}

func TestNewOrderReplaysItsCreationBlockWithNewRecipient(t *testing.T) {
	testCollectionDB(t)
	noPublicFallbacks(t, conf.Bsc)
	base := time.Now().Add(-200 * time.Second).Unix()
	a := testOrder(t, model.UsdtBep20, "0x"+strings.Repeat("1", 40), time.Unix(base+50, 0), time.Now().Add(time.Hour))
	b := testOrder(t, model.UsdtBep20, "0x"+strings.Repeat("2", 40), time.Unix(base+105, 0), time.Now().Add(time.Hour))
	node := &fakeEVM{chain: 56, baseTime: base}
	node.head.Store(120)
	server := httptest.NewServer(http.HandlerFunc(node.serve))
	defer server.Close()
	testSetting(t, model.RpcEndpointBsc, server.URL)
	e := evm{Network: conf.Bsc, Client: server.Client(), nextBlock: 121, startAt: base + 49, seenOrders: map[int64]bool{a.ID: true}}
	e.scanBlocks(context.Background())
	require.NotEmpty(t, node.ranges)
	require.Equal(t, int64(104), node.ranges[0][0])
	encoded, _ := json.Marshal(node.filters[0])
	require.Contains(t, string(encoded), b.MatchAddress[2:])
}

func TestRPCFailoverRejectsWrongChain(t *testing.T) {
	testCollectionDB(t)
	var primaryCalls, wrongData atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		http.Error(w, "paid subscription required", 403)
	}))
	defer primary.Close()
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call evmRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&call)
		if call.Method != "eth_chainId" {
			wrongData.Add(1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": "0x1"})
	}))
	defer wrong.Close()
	node := &fakeEVM{chain: 56}
	node.head.Store(1234)
	good := httptest.NewServer(http.HandlerFunc(node.serve))
	defer good.Close()
	old := conf.EVMNetworks[conf.Bsc]
	conf.EVMNetworks[conf.Bsc] = conf.EVMNetwork{ChainID: 56, URLs: []string{wrong.URL, good.URL}}
	defer func() { conf.EVMNetworks[conf.Bsc] = old }()
	testSetting(t, model.RpcEndpointBsc, primary.URL+"/private-key")
	e := evm{Network: conf.Bsc, Client: good.Client()}
	for range 2 {
		value, err := e.rpc(context.Background(), "eth_blockNumber", []any{})
		require.NoError(t, err)
		require.Equal(t, "0x4d2", value.String())
	}
	require.Equal(t, int64(1), primaryCalls.Load())
	require.Zero(t, wrongData.Load())
	require.True(t, conf.GetStats()[conf.Bsc].Failover)
}

func TestRPCRejectsMalformedAndIncompleteResponses(t *testing.T) {
	for _, body := range []string{"subscription required", `{"jsonrpc":"2.0","id":1,"result":null}`, `[{"jsonrpc":"2.0","id":1,"result":{"number":"0x1","timestamp":"0x1"}}]`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
			defer server.Close()
			calls := []evmRPCRequest{rpcRequest(1, "eth_getBlockByNumber", []any{"0x1", false}), rpcRequest(2, "eth_getBlockByNumber", []any{"0x2", false})}
			payload, _ := json.Marshal(calls)
			_, err := callEVMRPC(context.Background(), server.Client(), server.URL, payload, calls, true)
			require.Error(t, err)
		})
	}
}

func TestLookbackFailureAndFullQueueKeepProgress(t *testing.T) {
	testCollectionDB(t)
	order := testOrder(t, model.UsdtTrc20, "recipient", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	fail := true
	count := 0
	var heights []int64
	resolve := func(context.Context, int64, int64) (int64, int64, error) {
		count++
		if fail {
			return 0, 0, errors.New("timeout")
		}
		return 10, 15, nil
	}
	blocked := func() bool { return len(heights) >= 2 }
	enqueue := func(_ context.Context, from, to int64) error { heights = append(heights, from); return nil }
	replayOrders(context.Background(), conf.Tron, 1, 0, blocked, resolve, enqueue)
	_, done := lookbackDone.Load(order.ID)
	require.False(t, done)
	fail = false
	replayOrders(context.Background(), conf.Tron, 1, 0, blocked, resolve, enqueue)
	require.Equal(t, []int64{10, 11}, heights)
	_, done = lookbackDone.Load(order.ID)
	require.False(t, done)
	replayOrders(context.Background(), conf.Tron, 1, 0, func() bool { return false }, resolve, enqueue)
	require.Equal(t, []int64{10, 11, 12, 13, 14, 15}, heights)
	require.Equal(t, 2, count)
	_, done = lookbackDone.Load(order.ID)
	require.True(t, done)
	invalidateLookback(conf.Tron)
	_, _, ids, ok := pendingLookbackUnix(conf.Tron)
	require.True(t, ok)
	require.Contains(t, ids, order.ID)
}

func TestTransferClaimAndConfirmationAreIdempotent(t *testing.T) {
	testCollectionDB(t)
	a := testOrder(t, model.UsdtBep20, "0x"+strings.Repeat("1", 40), time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	stale := a
	b := testOrder(t, model.UsdtBep20, a.Address, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	require.NoError(t, a.MarkConfirming(123, "sender", "transaction", time.Now(), decimal.RequireFromString("1.49")))
	require.False(t, stale.SetExpired(), "an expiration snapshot cannot overwrite a detected transfer")
	require.Error(t, b.MarkConfirming(123, "sender", "transaction", time.Now(), decimal.RequireFromString("1.49")))
	require.NoError(t, a.SetSuccess())
	require.Error(t, a.SetSuccess())
	require.Error(t, a.MarkConfirming(124, "sender", "another-transaction", time.Now(), decimal.RequireFromString("1.49")))
	var saved model.Order
	require.NoError(t, model.Db.First(&saved, a.ID).Error)
	require.Equal(t, model.OrderStatusSuccess, saved.Status)
	require.Equal(t, "transaction", saved.RefHash)
}

func TestEthereumProbeChecksFreshHeadInsteadOfConfirmationBlock(t *testing.T) {
	testCollectionDB(t)
	noPublicFallbacks(t, conf.Ethereum)
	node := &fakeEVM{chain: 1, baseTime: time.Now().Unix() - 1200, blockSeconds: 12, missingTip: true}
	node.head.Store(100)
	server := httptest.NewServer(http.HandlerFunc(node.serve))
	defer server.Close()
	testSetting(t, model.RpcEndpointEthereum, server.URL)
	require.NoError(t, ProbeNetwork(context.Background(), conf.Ethereum), "30 Ethereum blocks take about six minutes; the current head is fresh")
	node.baseTime -= 3600
	require.ErrorContains(t, ProbeNetwork(context.Background(), conf.Ethereum), "not synchronized")
}

func TestRetiredDefaultMigrationPreservesCustomNodesAndSecrets(t *testing.T) {
	testCollectionDB(t)
	testSetting(t, model.RpcEndpointBsc, "https://binance-smart-chain-public.nodies.app/")
	testSetting(t, model.RpcEndpointEthereum, "https://rpc.example/private-key")
	testSetting(t, model.RpcEndpointBase, "https://base-public.nodies.app?apikey=custom")
	testSetting(t, model.RpcEndpointTronGridApiKey, "keep-this-secret")
	require.NoError(t, model.MigratePublicRPCDefaults())
	model.RefreshC()
	require.Equal(t, "https://bsc-rpc.publicnode.com", model.GetC(model.RpcEndpointBsc))
	require.Equal(t, "https://rpc.example/private-key", model.GetC(model.RpcEndpointEthereum))
	require.Equal(t, "https://base-public.nodies.app?apikey=custom", model.GetC(model.RpcEndpointBase))
	require.Equal(t, "keep-this-secret", model.GetC(model.RpcEndpointTronGridApiKey))
}

func TestEVMFallbackCooldownIsPerMethod(t *testing.T) {
	testCollectionDB(t)
	node := &fakeEVM{chain: 56, baseTime: time.Now().Unix() - 100}
	node.head.Store(100)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call evmRPCRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &call)
		if call.Method == "eth_getTransactionReceipt" {
			http.Error(w, "receipt forbidden", 403)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		node.serve(w, r)
	}))
	defer primary.Close()
	var receiptCalls atomic.Int64
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call evmRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&call)
		var result any
		switch call.Method {
		case "eth_chainId":
			result = "0x38"
		case "eth_getTransactionReceipt":
			receiptCalls.Add(1)
			result = nil
		default:
			http.Error(w, "logs unavailable", 403)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": result})
	}))
	defer fallback.Close()
	old := conf.EVMNetworks[conf.Bsc]
	conf.EVMNetworks[conf.Bsc] = conf.EVMNetwork{ChainID: 56, URLs: []string{fallback.URL}}
	defer func() { conf.EVMNetworks[conf.Bsc] = old }()
	testSetting(t, model.RpcEndpointBsc, primary.URL)
	e := evm{Network: conf.Bsc, Client: primary.Client()}
	// A fallback that rejects logs must remain usable for receipt confirmation.
	_, err := e.rpc(context.Background(), "unsupported_log_method", []any{})
	require.Error(t, err)
	_, err = e.rpc(context.Background(), "eth_getTransactionReceipt", []any{"0x" + strings.Repeat("a", 64)})
	require.NoError(t, err)
	_, err = e.rpc(context.Background(), "eth_getLogs", []any{map[string]any{"fromBlock": "0x50", "toBlock": "0x51", "address": evmContracts(conf.Bsc)}})
	require.NoError(t, err, "the primary's receipt restriction must not disable its working logs")
	require.Equal(t, int64(1), receiptCalls.Load())
}
