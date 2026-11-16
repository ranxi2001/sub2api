//go:build bps_request_integration

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/mihomo"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const bpsRequestCompletedWire = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_local\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"

type requestProxyObservation struct {
	id   int
	path string
	body []byte
}

// This adapter replaces the external HTTPS boundary with controlled local HTTP
// proxies. Selection, leases, request construction and bridge consumption stay
// on the public Forward path; no external host is resolved or contacted.
func localBPSRequestProxyFixture(t *testing.T, count int, handler func(int, http.ResponseWriter, *http.Request)) (*OpenAIGatewayService, *Account) {
	t.Helper()
	restore := mihomo.InstallBPSRequestIntegrationProbe(func(context.Context, string) error { return nil })
	t.Cleanup(restore)
	clients := make(map[string]*http.Client, count)
	proxies := make(map[int64]*Proxy, count)
	records := make([]mihomo.BPSStaticProxy, 0, count)
	ids := make([]int64, 0, count)
	for i := 1; i <= count; i++ {
		id := i
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler(id, w, r) }))
		t.Cleanup(proxy.Close)
		u, err := url.Parse(proxy.URL)
		require.NoError(t, err)
		port, err := strconv.Atoi(u.Port())
		require.NoError(t, err)
		proxies[int64(i)] = &Proxy{ID: int64(i), Protocol: "http", Host: u.Hostname(), Port: port, Status: StatusActive}
		tr := &http.Transport{Proxy: http.ProxyURL(u)}
		t.Cleanup(tr.CloseIdleConnections)
		clients[proxy.URL] = &http.Client{Transport: tr, Timeout: 10 * time.Second}
		ids = append(ids, int64(i))
		records = append(records, mihomo.BPSStaticProxy{ID: int64(i), URL: proxy.URL})
	}
	mihomo.SetBPSStaticProxyRecords(records)
	mihomo.SetBPSStaticRequestGroups(map[int64][]int64{300: ids})
	mihomo.WarmBPSPools(t.Context(), 0, 1)
	svc := openAIClientToolsTestService(nil)
	svc.proxyRepo = &openAIMaintenanceProxyRepo{proxies: proxies}
	svc.httpUpstream = &nativeAttachmentUpstream{do: func(r *http.Request, proxy string, _ int64, _ int) (*http.Response, error) {
		client := clients[proxy]
		if client == nil {
			return nil, fmt.Errorf("unexpected proxy outside selected fixture")
		}
		local := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = "http"
		local.URL = &u
		local.RequestURI = ""
		return client.Do(local)
	}}
	account := excelAccount()
	account.Concurrency = 6
	account.Extra["openai_excel_bps_proxy_mode"] = "request"
	account.Extra["openai_excel_bps_proxy_ids"] = ids
	account.Extra["openai_excel_bps_mihomo"] = false
	return svc, account
}

func forwardLocalBPSRequest(ctx context.Context, svc *OpenAIGatewayService, account *Account, body []byte) (int, string, error) {
	// Scheduler hydration gives each request its own Account model caches. Keep
	// that contract here while sharing the account ID for actual pool load.
	requestAccount := *account
	requestAccount.Extra = cloneExcelBPSRecoveryExtra(account.Extra)
	requestAccount.Credentials = cloneExcelBPSRecoveryExtra(account.Credentials)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	_, err := svc.Forward(ctx, c, &requestAccount, body)
	return rec.Code, rec.Body.String(), err
}

func TestExcelBPSRequestProxyForwardSameSessionDistributesSixThroughThree(t *testing.T) {
	arrived := make(chan int, 6)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	svc, account := localBPSRequestProxyFixture(t, 4, func(id int, w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		arrived <- id
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, bpsRequestCompletedWire)
	})
	account.Extra["openai_excel_bps_proxy_ids"] = []int64{1, 2, 3}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	results := make(chan error, 6)
	for i := 0; i < 6; i++ {
		go func() {
			code, response, err := forwardLocalBPSRequest(ctx, svc, account, []byte(`{"model":"gpt-6-astra","input":"test","stream":true,"prompt_cache_key":"same-session"}`))
			if err == nil && (code != 200 || !bytes.Contains([]byte(response), []byte("response.completed"))) {
				err = fmt.Errorf("incomplete local response: status %d", code)
			}
			results <- err
		}()
	}
	hits := map[int]int{}
	for i := 0; i < 6; i++ {
		select {
		case id := <-arrived:
			hits[id]++
		case <-ctx.Done():
			t.Fatal("six requests did not reach local proxies")
		}
	}
	releaseOnce.Do(func() { close(release) })
	for i := 0; i < 6; i++ {
		require.NoError(t, <-results)
	}
	require.Equal(t, map[int]int{1: 2, 2: 2, 3: 2}, hits)
}

func TestExcelBPSRequestProxyForwardAttachmentAndGenerationKeepOneExit(t *testing.T) {
	observed := make(chan requestProxyObservation, 4)
	svc, account := localBPSRequestProxyFixture(t, 3, func(id int, w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		observed <- requestProxyObservation{id: id, path: r.URL.Path, body: raw}
		if r.URL.Path == "/basispoints/api/attachments" {
			_, _ = io.WriteString(w, `{"openai_file_id":"file-local"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, bpsRequestCompletedWire)
	})
	enableNativeAttachments(svc)
	body, _ := nativeGatewayBody(t)
	code, _, err := forwardLocalBPSRequest(t.Context(), svc, account, body)
	require.NoError(t, err)
	require.Equal(t, 200, code)
	upload, generated := <-observed, <-observed
	require.Equal(t, "/basispoints/api/attachments", upload.path)
	require.Equal(t, "/basispoints/api/responses", generated.path)
	require.Equal(t, upload.id, generated.id)
	require.Contains(t, string(generated.body), `"file_id":"file-local"`)
	require.Empty(t, observed, "a logical request uploads once and generates once")
}

func TestExcelBPSRequestProxyForwardCancellationReleasesExitWhileOtherStreamsStayBusy(t *testing.T) {
	arrived := make(chan int, 4)
	svc, account := localBPSRequestProxyFixture(t, 3, func(id int, w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_busy\",\"status\":\"in_progress\"}}\n\n")
		w.(http.Flusher).Flush()
		arrived <- id
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	type request struct {
		cancel context.CancelFunc
		done   chan error
		proxy  int
	}
	start := func() request {
		child, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			_, _, err := forwardLocalBPSRequest(child, svc, account, []byte(`{"model":"gpt-6-astra","input":"hold","stream":true,"prompt_cache_key":"same-busy-session"}`))
			done <- err
		}()
		select {
		case id := <-arrived:
			return request{cancel: stop, done: done, proxy: id}
		case <-ctx.Done():
			stop()
			t.Fatal("local stream did not start")
			return request{}
		}
	}
	first, second, third := start(), start(), start()
	t.Cleanup(first.cancel)
	t.Cleanup(second.cancel)
	t.Cleanup(third.cancel)
	require.ElementsMatch(t, []int{1, 2, 3}, []int{first.proxy, second.proxy, third.proxy})
	first.cancel()
	select {
	case <-first.done:
	case <-ctx.Done():
		t.Fatal("canceled stream did not finish")
	}
	fourth := start()
	t.Cleanup(fourth.cancel)
	require.Equal(t, first.proxy, fourth.proxy, "canceled request releases its load; other selected exits remain in flight")
	second.cancel()
	third.cancel()
	fourth.cancel()
	for _, r := range []request{second, third, fourth} {
		select {
		case <-r.done:
		case <-ctx.Done():
			t.Fatal("remaining stream did not finish")
		}
	}
}

func TestExcelBPSRequestProxyForwardCompletionReleasesExitWhileOtherStreamsStayBusy(t *testing.T) {
	arrived := make(chan int, 4)
	upstreamClosed := make(chan struct{}, 2)
	var mu sync.Mutex
	calls := 0
	svc, account := localBPSRequestProxyFixture(t, 3, func(id int, w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		calls++
		order := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		arrived <- id
		if order <= 2 {
			_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_held\",\"status\":\"in_progress\"}}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, bpsRequestCompletedWire)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		upstreamClosed <- struct{}{}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	body := []byte(`{"model":"gpt-6-astra","input":"hold","stream":true}`)
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, _, err := forwardLocalBPSRequest(ctx, svc, account, body); done <- err }()
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("held request did not start")
		}
	}
	code, _, err := forwardLocalBPSRequest(ctx, svc, account, body)
	require.NoError(t, err)
	require.Equal(t, 200, code)
	completed := <-arrived
	code, _, err = forwardLocalBPSRequest(ctx, svc, account, body)
	require.NoError(t, err)
	require.Equal(t, 200, code)
	require.Equal(t, completed, <-arrived, "normal EOF/Close releases the completed stream, leaving the other two exits busy")
	for i := 0; i < 2; i++ {
		select {
		case <-upstreamClosed:
		case <-time.After(time.Second):
			t.Fatal("completed bridge must close upstream without waiting for upstream EOF")
		}
	}
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("held request did not finish")
		}
	}
}

func TestExcelBPSRequestProxyForwardRejectedRequestReleasesExit(t *testing.T) {
	arrived := make(chan int, 4)
	var mu sync.Mutex
	calls := 0
	svc, account := localBPSRequestProxyFixture(t, 3, func(id int, w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		calls++
		order := calls
		mu.Unlock()
		arrived <- id
		if order <= 2 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_held\",\"status\":\"in_progress\"}}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if order == 3 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"bad_request","message":"local fixture rejected"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, bpsRequestCompletedWire)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	body := []byte(`{"model":"gpt-6-astra","input":"hold","stream":true}`)
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, _, err := forwardLocalBPSRequest(ctx, svc, account, body); done <- err }()
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("held request did not start")
		}
	}
	code, _, err := forwardLocalBPSRequest(ctx, svc, account, body)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, code)
	rejected := <-arrived
	code, _, err = forwardLocalBPSRequest(ctx, svc, account, body)
	require.NoError(t, err)
	require.Equal(t, 200, code)
	require.Equal(t, rejected, <-arrived, "error-body consumption and Close release the rejected request's exit")
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("held request did not finish")
		}
	}
}

func TestExcelBPSRequestProxyForwardSupportedRepairKeepsOriginalExit(t *testing.T) {
	observed := make(chan requestProxyObservation, 3)
	var mu sync.Mutex
	calls := 0
	svc, account := localBPSRequestProxyFixture(t, 3, func(id int, w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		observed <- requestProxyObservation{id: id, path: r.URL.Path, body: raw}
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_, _ = io.WriteString(w, excelBPSInvalidCiphertext)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, bpsRequestCompletedWire)
	})
	code, _, err := forwardLocalBPSRequest(t.Context(), svc, account, excelBPSEncryptedHistoryRequest(true))
	require.NoError(t, err)
	require.Equal(t, 200, code)
	first, repaired := <-observed, <-observed
	require.Equal(t, first.id, repaired.id)
	require.Contains(t, string(first.body), "private-ciphertext")
	require.NotContains(t, string(repaired.body), "private-ciphertext")
	require.Empty(t, observed, "only the existing bounded application repair is submitted")
}
