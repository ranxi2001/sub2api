package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/v03413/bepusdt/app/conf"
)

const evmResponseLimit = 16 << 20

type evmRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

func rpcRequest(id int64, method string, params any) evmRPCRequest {
	return evmRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
}

type evmNodeState struct {
	verifiedUntil time.Time
	retryAt       time.Time
}

var evmNodes = struct {
	sync.Mutex
	states map[string]evmNodeState
}{states: make(map[string]evmNodeState)}

func (e *evm) rpc(ctx context.Context, method string, params any) (gjson.Result, error) {
	response, err := e.rpcBatch(ctx, []evmRPCRequest{rpcRequest(1, method, params)}, false)
	if err != nil {
		return gjson.Result{}, err
	}
	return response[0].Get("result"), nil
}

func (e *evm) rpcBatch(ctx context.Context, calls []evmRPCRequest, batch bool) ([]gjson.Result, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	var payload []byte
	if batch {
		payload, _ = json.Marshal(calls)
	} else {
		payload, _ = json.Marshal(calls[0])
	}
	primary := strings.TrimRight(e.rpcEndpoint(), "/")
	candidates := []string{primary}
	for _, endpoint := range conf.EVMNetworks[e.Network].URLs {
		if strings.TrimRight(endpoint, "/") != primary {
			candidates = append(candidates, endpoint)
		}
	}
	var failures []error
	for _, endpoint := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		identityKey := e.Network + "\x00" + endpoint
		key := identityKey + "\x00" + calls[0].Method
		evmNodes.Lock()
		state := evmNodes.states[key]
		identity := evmNodes.states[identityKey]
		evmNodes.Unlock()
		if time.Now().Before(state.retryAt) || time.Now().Before(identity.retryAt) {
			continue
		}
		nodeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		var err error
		if time.Now().After(identity.verifiedUntil) {
			check := rpcRequest(1, "eth_chainId", []any{})
			body, _ := json.Marshal(check)
			var response []gjson.Result
			response, err = callEVMRPC(nodeCtx, e.Client, endpoint, body, []evmRPCRequest{check}, false)
			if err == nil {
				chain, parseErr := hexInt(response[0].Get("result").String())
				if parseErr != nil || chain != conf.EVMNetworks[e.Network].ChainID || chain == 0 {
					err = errors.New("RPC returned a different chain ID")
				} else {
					identity.verifiedUntil = time.Now().Add(10 * time.Minute)
				}
			}
			if err != nil {
				identity.retryAt = time.Now().Add(30 * time.Second)
			}
			evmNodes.Lock()
			evmNodes.states[identityKey] = identity
			evmNodes.Unlock()
		}
		var response []gjson.Result
		if err == nil {
			response, err = callEVMRPC(nodeCtx, e.Client, endpoint, payload, calls, batch)
		}
		cancel()
		if err == nil {
			state.retryAt = time.Time{}
			evmNodes.Lock()
			evmNodes.states[key] = state
			evmNodes.Unlock()
			conf.RecordEndpoint(e.Network, endpointHost(endpoint), endpoint != primary)
			return response, nil
		}
		// Do not log URL paths or query strings: custom RPC URLs can carry keys.
		failures = append(failures, fmt.Errorf("%s %s: %w", endpointHost(endpoint), calls[0].Method, err))
		state.retryAt = time.Now().Add(30 * time.Second)
		evmNodes.Lock()
		evmNodes.states[key] = state
		evmNodes.Unlock()
	}
	if len(failures) == 0 {
		return nil, errors.New("RPC nodes are cooling down after failures")
	}
	return nil, errors.Join(failures...)
}

func callEVMRPC(ctx context.Context, client *http.Client, endpoint string, payload []byte, calls []evmRPCRequest, batch bool) ([]gjson.Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("invalid RPC URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("RPC connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("RPC HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, evmResponseLimit+1))
	if err != nil || len(body) > evmResponseLimit || !gjson.ValidBytes(body) {
		return nil, errors.New("invalid or oversized RPC response")
	}
	parsed := gjson.ParseBytes(body)
	responses := []gjson.Result{parsed}
	if batch {
		if !parsed.IsArray() {
			return nil, errors.New("RPC batch response is not an array")
		}
		responses = parsed.Array()
	}
	if len(responses) != len(calls) {
		return nil, errors.New("RPC batch response is incomplete")
	}
	expected := make(map[int64]evmRPCRequest, len(calls))
	for _, call := range calls {
		expected[call.ID] = call
	}
	for _, response := range responses {
		call, exists := expected[response.Get("id").Int()]
		if !exists || !response.IsObject() || response.Get("jsonrpc").String() != "2.0" {
			return nil, errors.New("invalid RPC response identity")
		}
		delete(expected, call.ID)
		if response.Get("error").Exists() {
			return nil, fmt.Errorf("RPC rejected %s (code %d)", call.Method, response.Get("error.code").Int())
		}
		result := response.Get("result")
		if !result.Exists() && result.Raw != "null" {
			return nil, errors.New("RPC response has no result")
		}
		switch call.Method {
		case "eth_blockNumber", "eth_chainId":
			if n, err := hexInt(result.String()); err != nil || n <= 0 {
				return nil, fmt.Errorf("invalid %s result", call.Method)
			}
		case "eth_getLogs":
			if !result.IsArray() {
				return nil, errors.New("RPC log result is not an array")
			}
		case "eth_getBlockByNumber":
			if !result.IsObject() || result.Get("number").String() == "" || result.Get("timestamp").String() == "" {
				return nil, errors.New("RPC block is unavailable")
			}
		case "eth_getTransactionReceipt":
			if result.Raw != "null" && !result.IsObject() {
				return nil, errors.New("invalid transaction receipt")
			}
		}
	}
	return responses, nil
}

func hexInt(value string) (int64, error) {
	if !strings.HasPrefix(value, "0x") || len(value) < 3 {
		return 0, errors.New("invalid hex quantity")
	}
	return strconv.ParseInt(value[2:], 16, 64)
}

func endpointHost(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" {
		return "configured node"
	}
	return parsed.Hostname()
}
