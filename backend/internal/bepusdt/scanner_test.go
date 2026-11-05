//go:build unit

package bepusdt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	beconf "github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/task"
)

type scannerTransport func(*http.Request) (*http.Response, error)

func (f scannerTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type scannerRPCCall struct {
	ID     any               `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

// Upstream workers have process-global queues and do not provide a join API.
// Run the real scanner in a subprocess so their DB cannot leak into other tests.
func TestBEpusdtEmbeddedScanner(t *testing.T) {
	if os.Getenv("SUB2API_TEST_SCANNER_DIR") == "" {
		binary, err := os.Executable()
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestBEpusdtEmbeddedScanner$", "-test.v")
		cmd.Env = append(os.Environ(), "SUB2API_TEST_SCANNER_DIR="+t.TempDir())
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		return
	}
	_, err := InitializeForTest(os.Getenv("SUB2API_TEST_SCANNER_DIR"))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var head atomic.Int64
	head.Store(2002)
	var receiptRequests atomic.Int64
	callbacks := make(chan map[string]any, 4)
	blockTime := time.Now().Add(2 * time.Second).Unix()
	address := "0x1111111111111111111111111111111111111111"
	hash := "0x" + strings.Repeat("a", 64)
	blockHash := fmt.Sprintf("0x%064x", 2000)
	transferLog := map[string]any{"address": beconf.UsdcBase, "blockNumber": "0x7d0", "blockHash": blockHash, "transactionHash": hash,
		"topics": []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", "0x" + strings.Repeat("0", 24) + strings.Repeat("2", 40), "0x" + strings.Repeat("0", 24) + address[2:]},
		"data":   fmt.Sprintf("0x%064x", 28880000)}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/notify" {
			var callback map[string]any
			if json.NewDecoder(r.Body).Decode(&callback) == nil && callback["status"] == float64(model.OrderStatusSuccess) {
				select {
				case callbacks <- callback:
				default:
				}
			}
			_, _ = io.WriteString(w, "success")
			return
		}
		body, _ := io.ReadAll(r.Body)
		var calls []scannerRPCCall
		batch := len(body) > 0 && body[0] == '['
		if batch {
			_ = json.Unmarshal(body, &calls)
		} else {
			calls = make([]scannerRPCCall, 1)
			_ = json.Unmarshal(body, &calls[0])
		}
		results := make([]map[string]any, 0, len(calls))
		for _, call := range calls {
			var result any
			switch call.Method {
			case "eth_chainId":
				result = "0x2105"
			case "eth_blockNumber":
				result = fmt.Sprintf("0x%x", head.Load())
			case "eth_getBlockByNumber":
				var number string
				_ = json.Unmarshal(call.Params[0], &number)
				height, _ := strconv.ParseInt(strings.TrimPrefix(number, "0x"), 16, 64)
				result = map[string]any{"number": number, "hash": fmt.Sprintf("0x%064x", height), "timestamp": fmt.Sprintf("0x%x", blockTime+height-2000), "transactions": []any{}}
			case "eth_getLogs":
				var filter struct {
					From    string   `json:"fromBlock"`
					To      string   `json:"toBlock"`
					Address []string `json:"address"`
				}
				_ = json.Unmarshal(call.Params[0], &filter)
				from, _ := strconv.ParseInt(strings.TrimPrefix(filter.From, "0x"), 16, 64)
				to, _ := strconv.ParseInt(strings.TrimPrefix(filter.To, "0x"), 16, 64)
				result = []any{}
				if len(filter.Address) == 0 {
					http.Error(w, "address filter required", 400)
					return
				}
				if from <= 2000 && to >= 2000 {
					result = []any{transferLog}
				}
			case "eth_getTransactionReceipt":
				receiptRequests.Add(1)
				result = map[string]any{"status": "0x1", "transactionHash": hash, "blockNumber": "0x7d0", "blockHash": blockHash, "logs": []any{transferLog}}
			default:
				http.Error(w, "unexpected RPC", http.StatusBadRequest)
				return
			}
			results = append(results, map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": result})
		}
		if batch {
			_ = json.NewEncoder(w).Encode(results)
		} else {
			_ = json.NewEncoder(w).Encode(results[0])
		}
	}))
	defer server.Close()
	// Stub the upstream lookback block-time helper; default clients cannot
	// make any real Internet requests in this test. The Base RPC is local.
	transport := http.DefaultTransport
	http.DefaultTransport = scannerTransport(func(req *http.Request) (*http.Response, error) {
		if strings.HasPrefix(req.URL.String(), server.URL+"/") {
			return transport.RoundTrip(req)
		}
		if req.URL.Hostname() == "api.bepusdt.online" {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"start":2000,"end":2000}`)), Request: req}, nil
		}
		return nil, fmt.Errorf("unexpected external request: %s", req.URL.Hostname())
	})
	require.NoError(t, SaveSettings(ctx, map[string]string{"rpc_endpoint_base": server.URL}))
	require.NoError(t, SaveWallet(ctx, model.Wallet{Address: address, TradeType: "usdc.base", Status: 1}))
	require.NoError(t, SaveRate(ctx, "USD", "USDC", "1"))
	order, err := Create(ctx, CreateRequest{OrderID: "scanner-order", Amount: "28.88", TradeType: "usdc.base", Currency: "USD",
		NotifyURL: server.URL + "/notify", ReturnURL: server.URL + "/result", ExpiresAt: time.Now().Add(5 * time.Minute)})
	require.NoError(t, err)
	require.NoError(t, task.Init())
	task.Start(ctx)
	require.Eventually(t, func() bool {
		current, err := Order(ctx, order.TradeId)
		return err == nil && current.Status == model.OrderStatusConfirming
	}, 15*time.Second, 100*time.Millisecond)
	// Keep the head below Base's 40-block depth for an entire confirmation tick.
	time.Sleep(6 * time.Second)
	current, err := Order(ctx, order.TradeId)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusConfirming, current.Status)
	require.Zero(t, receiptRequests.Load())
	head.Store(2040)
	require.Eventually(t, func() bool {
		current, err := Order(ctx, order.TradeId)
		return err == nil && current.Status == model.OrderStatusSuccess
	}, 15*time.Second, 100*time.Millisecond)
	select {
	case callback := <-callbacks:
		require.Equal(t, "scanner-order", callback["order_id"])
		require.Equal(t, float64(2), callback["status"])
		require.Equal(t, "28.88", callback["actual_amount"])
		require.NotEmpty(t, callback["signature"])
	case <-time.After(3 * time.Second):
		t.Fatal("confirmed payment did not emit a callback")
	}
}
