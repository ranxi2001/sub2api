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
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/utils"
)

const solanaMainnetGenesis = "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d"

var nativeFallbacks = map[string][]string{
	conf.Solana: {"https://solana-rpc.publicnode.com", "https://api.mainnet-beta.solana.com"},
	conf.Aptos:  {"https://aptos-rest.publicnode.com", "https://fullnode.mainnet.aptoslabs.com"},
}

type nativeTransport struct {
	network string
	base    http.RoundTripper
}

func (t *nativeTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func newNativeHTTPClient(network string) *http.Client {
	client := utils.NewHttpClient()
	client.Transport = &nativeTransport{network: network, base: client.Transport}
	return client
}

// Retry only read requests, preserving the body and validating each chain.
// Credentials in a custom primary URL are never copied to a public fallback.
func (t *nativeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var payload []byte
	if req.Body != nil {
		var err error
		payload, err = io.ReadAll(io.LimitReader(req.Body, 1<<20))
		_ = req.Body.Close()
		if err != nil {
			return nil, errors.New("unable to read node request")
		}
	}
	primary := strings.TrimRight(model.Endpoint(model.Network(t.network)), "/")
	endpoints := []string{primary}
	for _, fallback := range nativeFallbacks[t.network] {
		if fallback != primary {
			endpoints = append(endpoints, fallback)
		}
	}
	for _, endpoint := range endpoints {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		key := t.network + "\x00" + endpoint
		evmNodes.Lock()
		state := evmNodes.states[key]
		evmNodes.Unlock()
		if time.Now().Before(state.retryAt) {
			continue
		}
		ctx, cancel := context.WithTimeout(req.Context(), 8*time.Second)
		var err error
		if time.Now().After(state.verifiedUntil) {
			err = t.verify(ctx, endpoint)
			if err == nil {
				state.verifiedUntil = time.Now().Add(10 * time.Minute)
			}
		}
		var resp *http.Response
		if err == nil {
			clone := req.Clone(ctx)
			if endpoint != primary {
				clone.URL, err = nativeFallbackURL(req.URL, primary, endpoint, t.network)
				clone.Host = ""
				clone.Header = make(http.Header)
				clone.Header.Set("Content-Type", "application/json")
			}
			if err == nil {
				clone.Body = http.NoBody
				if len(payload) > 0 {
					clone.Body = io.NopCloser(bytes.NewReader(payload))
				}
				resp, _, err = t.read(clone)
			}
		}
		cancel()
		if err == nil {
			state.retryAt = time.Time{}
			evmNodes.Lock()
			evmNodes.states[key] = state
			evmNodes.Unlock()
			conf.RecordEndpoint(t.network, endpointHost(endpoint), endpoint != primary)
			resp.Request = req
			return resp, nil
		}
		state.retryAt = time.Now().Add(30 * time.Second)
		evmNodes.Lock()
		evmNodes.states[key] = state
		evmNodes.Unlock()
	}
	return nil, fmt.Errorf("%s nodes are unavailable; retrying", t.network)
}

func nativeFallbackURL(original *url.URL, primary, fallback, network string) (*url.URL, error) {
	u, err := url.Parse(fallback)
	if err != nil {
		return nil, err
	}
	if network == conf.Aptos {
		p, err := url.Parse(primary)
		if err != nil {
			return nil, err
		}
		suffix := strings.TrimPrefix(original.Path, strings.TrimRight(p.Path, "/"))
		u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(suffix, "/")
		query := u.Query()
		for _, key := range []string{"start", "limit", "with_transactions"} {
			if value := original.Query().Get(key); value != "" {
				query.Set(key, value)
			}
		}
		u.RawQuery = query.Encode()
	}
	return u, nil
}

func (t *nativeTransport) verify(ctx context.Context, endpoint string) error {
	method := http.MethodPost
	target := endpoint
	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"getGenesisHash","params":[]}`)
	if t.network == conf.Aptos {
		method = http.MethodGet
		target = strings.TrimRight(endpoint, "/") + "/v1"
		payload = nil
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, body, err := t.read(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if t.network == conf.Solana && body.Get("result").String() != solanaMainnetGenesis {
		return errors.New("different Solana network")
	}
	if t.network == conf.Aptos && body.Get("chain_id").Int() != 1 {
		return errors.New("different Aptos network")
	}
	return nil
}

func (t *nativeTransport) read(req *http.Request) (*http.Response, gjson.Result, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, gjson.Result{}, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20+1))
	_ = resp.Body.Close()
	if err != nil || len(body) > 32<<20 || resp.StatusCode < 200 || resp.StatusCode >= 300 || !gjson.ValidBytes(body) {
		return nil, gjson.Result{}, errors.New("invalid node response")
	}
	parsed := gjson.ParseBytes(body)
	if parsed.Get("error").Exists() {
		code := parsed.Get("error.code").Int()
		// Known skipped Solana slots are valid negative answers, not an outage.
		if t.network != conf.Solana || (code != -32007 && code != -32009) {
			return nil, gjson.Result{}, errors.New("node rejected request")
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp, parsed, nil
}

func nodeJSON(ctx context.Context, client *http.Client, method, target string, payload any) (gjson.Result, error) {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return gjson.Result{}, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return gjson.Result{}, errors.New("invalid node URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return gjson.Result{}, errors.New("node request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20+1))
	if err != nil || len(raw) > 32<<20 || resp.StatusCode != 200 || !gjson.ValidBytes(raw) {
		return gjson.Result{}, errors.New("invalid node response")
	}
	result := gjson.ParseBytes(raw)
	if result.Get("error").Exists() {
		return result, errors.New("node returned an RPC error")
	}
	return result, nil
}
