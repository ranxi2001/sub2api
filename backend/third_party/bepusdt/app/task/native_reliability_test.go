package task

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/smallnest/chanx"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/model"
)

func TestNativeFailoverValidatesNetworkAndDoesNotForwardCredentials(t *testing.T) {
	for _, network := range []string{conf.Solana, conf.Aptos} {
		t.Run(network, func(t *testing.T) {
			testCollectionDB(t)
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "subscription required", http.StatusForbidden)
			}))
			defer primary.Close()
			wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if network == conf.Solana {
					_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"testnet"}`))
				} else {
					_, _ = w.Write([]byte(`{"chain_id":2}`))
				}
			}))
			defer wrong.Close()
			good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Empty(t, r.URL.Query().Get("apikey"))
				require.Empty(t, r.Header.Get("Authorization"))
				require.NotContains(t, r.URL.Path, "private")
				if network == conf.Solana {
					var call evmRPCRequest
					require.NoError(t, json.NewDecoder(r.Body).Decode(&call))
					var result any = 500
					if call.Method == "getGenesisHash" {
						result = solanaMainnetGenesis
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
				} else if r.URL.Path == "/v1" {
					_, _ = w.Write([]byte(`{"chain_id":1}`))
				} else {
					require.Equal(t, "/v1/blocks/by_version/12", r.URL.Path)
					require.Equal(t, "false", r.URL.Query().Get("with_transactions"))
					_, _ = w.Write([]byte(`{"block_timestamp":"1234567890000000"}`))
				}
			}))
			defer good.Close()
			old := nativeFallbacks[network]
			nativeFallbacks[network] = []string{wrong.URL, good.URL}
			defer func() { nativeFallbacks[network] = old }()
			key := model.RpcEndpointSolana
			if network == conf.Aptos {
				key = model.RpcEndpointAptos
			}
			testSetting(t, key, primary.URL+"/private")
			client := newNativeHTTPClient(network)
			defer client.CloseIdleConnections()
			path := primary.URL + "/private?apikey=secret"
			var body any = rpcRequest(1, "getSlot", []any{})
			method := http.MethodPost
			if network == conf.Aptos {
				path = primary.URL + "/private/v1/blocks/by_version/12?apikey=secret&with_transactions=false"
				body = nil
				method = http.MethodGet
			}
			value, err := nodeJSON(context.Background(), client, method, path, body)
			require.NoError(t, err)
			if network == conf.Solana {
				require.Equal(t, int64(500), value.Get("result").Int())
			} else {
				require.Positive(t, value.Get("block_timestamp").Int())
			}
			require.True(t, conf.GetStats()[network].Failover)
		})
	}
}

func TestSolanaVersionOneLoadedAddressesAndFailedTransfers(t *testing.T) {
	testCollectionDB(t)
	old := nativeFallbacks[conf.Solana]
	nativeFallbacks[conf.Solana] = nil
	defer func() { nativeFallbacks[conf.Solana] = old }()
	ctx, cancel := context.WithCancel(context.Background())
	oldQueue := transferQueue
	transferQueue = chanx.NewUnboundedChan[[]transfer](ctx, 10)
	t.Cleanup(func() { cancel(); transferQueue = oldQueue })
	data := make([]byte, 9)
	data[0] = 3
	binary.LittleEndian.PutUint64(data[1:], 1490000)
	transaction := func(failed bool) map[string]any {
		var txError any
		if failed {
			txError = map[string]any{"InstructionError": []any{0, "failed"}}
		}
		balance := func(index int, owner string) map[string]any {
			return map[string]any{"accountIndex": index, "owner": owner, "mint": conf.UsdtSolana, "programId": conf.SolSplToken}
		}
		return map[string]any{
			"version":     1,
			"transaction": map[string]any{"signatures": []string{"transfer-signature"}, "message": map[string]any{"accountKeys": []string{"sender-token", "authority"}, "instructions": []any{map[string]any{"programIdIndex": 3, "accounts": []int{0, 2, 1}, "data": base58.Encode(data)}}}},
			"meta":        map[string]any{"err": txError, "loadedAddresses": map[string]any{"writable": []string{"recipient-token"}, "readonly": []string{conf.SolSplToken}}, "preTokenBalances": []any{balance(0, "sender")}, "postTokenBalances": []any{balance(0, "sender"), balance(2, "recipient")}},
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&call))
		var result any = solanaMainnetGenesis
		if call.Method == "getBlock" {
			var options map[string]any
			require.NoError(t, json.Unmarshal(call.Params[1], &options))
			require.Equal(t, float64(1), options["maxSupportedTransactionVersion"])
			result = map[string]any{"blockTime": time.Now().Unix(), "transactions": []any{transaction(true), transaction(false)}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer server.Close()
	testSetting(t, model.RpcEndpointSolana, server.URL)
	scanner := newSolana()
	require.True(t, scanner.slotParse(100))
	select {
	case transfers := <-transferQueue.Out:
		require.Len(t, transfers, 1)
		require.Equal(t, "recipient", transfers[0].RecvAddress)
		require.Equal(t, "1.49", transfers[0].Amount.String())
	case <-time.After(time.Second):
		t.Fatal("valid version-one transfer was lost")
	}
	for _, instruction := range []string{`{"accounts":[0,99,1],"data":"` + base58.Encode(data) + `"}`, `{"accounts":[0,-1,1],"data":"` + base58.Encode(data) + `"}`} {
		require.Empty(t, scanner.parseTransfer(gjson.Parse(instruction), []string{"a", "b"}, nil).RecvAddress)
	}
}
