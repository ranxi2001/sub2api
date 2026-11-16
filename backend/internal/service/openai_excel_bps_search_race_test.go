package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Pause the real Forward caller immediately before result metadata is read.
// The repair HTTP call must enter before either side can proceed. Closing
// release precedes BOTH the caller read and the callback's body replacement;
// it deliberately does not order those two accesses or hide their old race.
type issue871RepairRaceContext struct {
	context.Context
	armed         atomic.Bool
	metadataGate  atomic.Bool
	repairEntered chan struct{}
	release       chan struct{}
}

func (c *issue871RepairRaceContext) Value(key any) any {
	if _, ok := key.(requestedReasoningEffortContextKey); ok && c.armed.Load() && c.metadataGate.CompareAndSwap(false, true) {
		select {
		case <-c.repairEntered:
		case <-c.Done():
		}
		close(c.release)
	}
	return c.Context.Value(key)
}

func TestExcelBPSSearchRepairOwnershipRaceThroughForward(t *testing.T) {
	for _, repair := range []string{"transport", "unknown_target"} {
		for _, outcome := range []string{"success", "failed"} {
			for _, mode := range []string{"json", "sse", "compact_keepalive"} {
				t.Run(repair+"/"+outcome+"/"+mode, func(t *testing.T) {
					parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					gate := &issue871RepairRaceContext{Context: parent, repairEntered: make(chan struct{}), release: make(chan struct{})}
					var calls atomic.Int64
					var requestsMu sync.Mutex
					var bodies [][]byte
					var paths, grants []string
					firstCode := "text(42);"
					if repair == "unknown_target" {
						firstCode = `{"name":"missing","arguments":{}}`
					}
					initial := excelBPSRepairWire(t, "original", "Run client tool", firstCode)
					invalid := excelBPSRepairWire(t, "invalid", "Run client tool", "text(42);")
					valid := excelBPSRepairWire(t, "fixed", "codex2api.custom/functions.exec", "text(42);")
					failed := "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":1},\"error\":{\"code\":\"basispoints_search_failed\",\"search_diagnostic\":{\"phase\":\"search_http\",\"reason\":\"http_rejected\",\"upstream_status\":403}}}}\n\n"
					upstream := &bpsTestUpstream{send: func(req *http.Request, _ string) (*http.Response, error) {
						raw, err := io.ReadAll(req.Body)
						_ = req.Body.Close()
						if err != nil {
							return nil, err
						}
						requestsMu.Lock()
						bodies = append(bodies, raw)
						paths = append(paths, req.URL.Path)
						grants = append(grants, req.Header.Get("Authorization"))
						requestsMu.Unlock()
						n := calls.Add(1)
						if n == 1 {
							gate.armed.Store(true)
							return issue871HTTP(200, initial), nil
						}
						if n == 2 {
							close(gate.repairEntered)
							select {
							case <-gate.release:
							case <-req.Context().Done():
								return nil, req.Context().Err()
							}
							if outcome == "failed" {
								return issue871HTTP(200, failed), nil
							}
							if repair == "transport" {
								return issue871HTTP(200, invalid), nil
							}
							return issue871HTTP(200, valid), nil
						}
						if n == 3 && repair == "transport" && outcome == "success" {
							return issue871HTTP(200, valid), nil
						}
						return nil, fmt.Errorf("unexpected repair/replay send %d", n)
					}}
					svc := openAIClientToolsTestService(nil)
					svc.httpUpstream = upstream
					account := excelAccount()
					account.Extra["openai_excel_bps_auto_disable_on_403"] = true
					body := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","stream":%t,"input":"test correction ownership","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}]}`, mode == "sse"))
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(gate)
					if mode == "compact_keepalive" {
						MarkOpenAICompactClientStream(c)
						stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
						defer stop()
						value, ok := c.Get(openAICompactSSEKeepaliveKey)
						require.True(t, ok)
						keepalive, ok := value.(*openAICompactSSEKeepalive)
						require.True(t, ok)
						require.True(t, keepalive.beat())
					}
					result, err := svc.Forward(gate, c, account, body)
					require.True(t, gate.metadataGate.Load(), "caller must reach the metadata rendezvous")
					require.NoError(t, parent.Err(), "repair must enter while caller metadata is paused")
					require.NotNil(t, result)
					wantCalls, wantInput, wantOutput := 2, 20, 4
					if outcome == "failed" {
						require.Error(t, err)
						wantInput, wantOutput = 17, 3
						detail := issue871Error(t, rec.Body.String(), mode != "json")
						require.Equal(t, "basispoints_search_failed", detail.Get("code").String())
						require.NotContains(t, detail.Raw, "403")
						require.NotContains(t, detail.Raw, "upstream_status")
						ops, ok := GetOpsStreamError(c)
						require.True(t, ok)
						require.Equal(t, 502, ops.IntendedStatus)
					} else {
						require.NoError(t, err)
						require.Contains(t, rec.Body.String(), "text(42);")
						if repair == "transport" {
							wantCalls, wantInput, wantOutput = 3, 30, 6
						}
					}
					require.Equal(t, int64(wantCalls), calls.Load())
					require.Equal(t, wantInput, result.Usage.InputTokens)
					require.Equal(t, wantOutput, result.Usage.OutputTokens)
					requestsMu.Lock()
					sentBodies := append([][]byte(nil), bodies...)
					sentPaths := append([]string(nil), paths...)
					sentGrants := append([]string(nil), grants...)
					requestsMu.Unlock()
					for i, sent := range sentBodies {
						require.Equal(t, "/basispoints/api/responses", sentPaths[i], "no search or replay endpoint")
						require.Equal(t, "Bearer test-token", sentGrants[i])
						require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(sent, "model").String())
						if repair == "transport" {
							require.Equal(t, fmt.Sprint(i+1), gjson.GetBytes(sent, "metadata.agent_iteration").String())
						}
					}
					if wantCalls == 3 {
						require.Contains(t, string(sentBodies[2]), "call_original", "second correction must retain the first correction's request history")
						require.Contains(t, string(sentBodies[2]), "call_invalid")
					}
					require.True(t, account.IsExcelBPSEnabled())
					require.True(t, account.Schedulable)
					require.False(t, svc.isExcelBPSCoolingDown(account, "gpt-5.6-sol"))
				})
			}
		}
	}
}
