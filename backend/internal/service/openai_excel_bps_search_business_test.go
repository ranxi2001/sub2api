package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const issue871BusinessQuery = "synthetic \"search\"\n第二行"
const issue871BusinessResult = `{"output":"Synthetic result","results":[{"url":"https://example.invalid/source","title":"Synthetic source"}]}`
const issue871BusinessAnswer = "见 https://example.invalid/source"

func issue871BusinessCall(t *testing.T, form string, round int) map[string]any {
	t.Helper()
	query, _ := json.Marshal(map[string]any{"query": issue871BusinessQuery})
	call := issue871Call(fmt.Sprintf("fc_search_%d", round), fmt.Sprintf("call_search_%d", round), "__sub2api_web_search")
	if strings.HasPrefix(form, "direct") {
		call["name"], call["arguments"] = "__sub2api_web_search", string(query)
	} else {
		code, _ := json.Marshal(map[string]any{"name": "__sub2api_web_search", "arguments": json.RawMessage(query)})
		args, _ := json.Marshal(map[string]any{"code": string(code), "summary": "Search", "extended_summary": "{}", "destructive": false, "references": []any{}})
		call["arguments"] = string(args)
	}
	if strings.HasSuffix(form, "_prefixed") {
		name, ok := call["name"].(string)
		require.True(t, ok, "search fixture tool name must be a string")
		call["name"] = "functions." + name
	}
	return call
}

// This supplier enforces the existing bridge's native history contract. It is
// an offline fixture, not evidence of the current live alpha endpoint contract.
// No assertions run on the bridge/search goroutines; requests are copied under
// a lock and checked by the test after Forward returns.
type issue871BusinessSupplier struct {
	mu       sync.Mutex
	bodies   [][]byte
	requests []*http.Request
	proxies  []string
	form     string
	failure  string
	calls    [2]map[string]any
}

func (s *issue871BusinessSupplier) send(req *http.Request, proxy string) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies = append(s.bodies, raw)
	s.requests = append(s.requests, req)
	s.proxies = append(s.proxies, proxy)
	n := len(s.bodies)
	if n == 1 {
		call := s.calls[0]
		if s.failure == "invalid_arguments" {
			call["arguments"] = `{"query":123}`
		}
		if s.failure == "unknown_tool" {
			call["name"] = "undeclared_search"
		}
		return issue871Round("resp_business", call), nil
	}
	if n == 2 || n == 4 {
		if req.URL.Path != "/backend-api/codex/alpha/search" || gjson.GetBytes(raw, "commands.search_query.0.q").String() != issue871BusinessQuery {
			return issue871HTTP(400, `{"error":"invalid synthetic search request"}`), nil
		}
		switch s.failure {
		case "search_transport":
			return nil, errors.New(issue871Private)
		case "search_403":
			return issue871HTTP(403, issue871Private), nil
		case "search_429":
			return issue871HTTP(429, issue871Private), nil
		case "search_parse":
			return issue871HTTP(200, `{"results":{},"output":"PRIVATE_BODY"}`), nil
		}
		return issue871HTTP(200, issue871BusinessResult), nil
	}
	if n != 3 && n != 5 {
		return nil, errors.New("unexpected synthetic supplier submission")
	}
	// Fail the business request when a server-only name is sent as a native
	// tool. The old implementation reaches this rejection after a valid search.
	for _, item := range gjson.GetBytes(raw, "input").Array() {
		if item.Get("type").String() == "function_call" {
			name := item.Get("name").String()
			if name != "run_officejs" && name != "functions.run_officejs" {
				return issue871HTTP(400, `{"error":"non-native tool in continuation"}`), nil
			}
		}
	}
	switch s.failure {
	case "continuation_403":
		return issue871HTTP(403, issue871Private), nil
	case "continuation_eof":
		return issue871HTTP(200, ""), nil
	case "repeated_call":
		call := s.calls[1]
		call["call_id"] = "call_search_0"
		return issue871Round("resp_repeat", call), nil
	}
	if n == 3 {
		return issue871Round("resp_second_search", s.calls[1]), nil
	}
	return issue871Round("resp_answer", map[string]any{"type": "message", "id": "msg_answer", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": issue871BusinessAnswer}}}), nil
}

func issue871BusinessForward(t *testing.T, supplier *issue871BusinessSupplier, mode string) (*OpenAIGatewayService, *Account, *gin.Context, *httptest.ResponseRecorder, *OpenAIForwardResult, error) {
	t.Helper()
	// Build and assert fixtures on the test goroutine before Forward starts.
	for round := range supplier.calls {
		supplier.calls[round] = issue871BusinessCall(t, supplier.form, round)
	}
	svc := openAIClientToolsTestService(nil)
	svc.httpUpstream = &bpsTestUpstream{send: supplier.send}
	account := excelAccount()
	account.Extra[ExcelBPSWebSearchKey] = true
	account.Extra["openai_excel_bps_auto_disable_on_403"] = true
	account.Extra["openai_excel_bps_mihomo"] = false
	account.Proxy = &Proxy{Protocol: "http", Host: "127.0.0.1", Port: 7890}
	body := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","stream":%t,"input":[{"role":"user","content":"Search synthetic sources"}],"tools":[{"type":"web_search","search_context_size":"high"}],"tool_choice":{"type":"web_search"}}`, mode == "sse"))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	c.Request.Header.Set("Cookie", "PRIVATE_COOKIE")
	c.Request.Header.Set("x-codex-turn-state", "PRIVATE_TICKET")
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
	result, err := svc.Forward(c.Request.Context(), c, account, body)
	return svc, account, c, rec, result, err
}

func TestExcelBPSSearchBusinessThroughForward(t *testing.T) {
	for _, form := range []string{"wrapped", "wrapped_prefixed", "direct", "direct_prefixed"} {
		for _, mode := range []string{"json", "sse"} {
			t.Run(form+"/"+mode, func(t *testing.T) {
				supplier := &issue871BusinessSupplier{form: form}
				_, account, _, rec, result, err := issue871BusinessForward(t, supplier, mode)
				supplier.mu.Lock()
				defer supplier.mu.Unlock()
				require.GreaterOrEqual(t, len(supplier.bodies), 3, "must reach continuation after a real mock search")
				first := gjson.GetBytes(supplier.bodies[2], "input").Array()
				require.GreaterOrEqual(t, len(first), 2)
				wantName := "run_officejs"
				if form == "wrapped_prefixed" {
					wantName = "functions.run_officejs"
				}
				// The remote RED gate accepts only this precise wire regression.
				if got := first[len(first)-2].Get("name").String(); got != wantName {
					t.Fatalf("issue871_expected_native_search_history: got %q; want %q", got, wantName)
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Len(t, supplier.requests, 5, "two searches and two continuations, without replay")
				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, 30, result.Usage.InputTokens)
				require.Equal(t, 6, result.Usage.OutputTokens)
				require.Equal(t, "response.completed", result.UpstreamTerminalEvent)
				initialInput := gjson.GetBytes(supplier.bodies[0], "input").Array()
				require.Contains(t, string(supplier.bodies[0]), "__sub2api_web_search")
				require.False(t, gjson.GetBytes(supplier.bodies[0], "tools").Exists(), "hosted declaration was normalized")
				for i, req := range supplier.requests {
					require.Equal(t, http.MethodPost, req.Method)
					require.Equal(t, "Bearer test-token", req.Header.Get("Authorization"))
					require.Equal(t, "test-account", req.Header.Get("Chatgpt-Account-Id"))
					require.Empty(t, req.Header.Get("Cookie"))
					require.Empty(t, req.Header.Get("x-codex-turn-state"))
					require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
					require.Equal(t, account.Proxy.URL(), supplier.proxies[i])
					require.Equal(t, "gpt-6-astra", gjson.GetBytes(supplier.bodies[i], "model").String())
					if i == 1 || i == 3 {
						require.Equal(t, "chatgpt.com", req.URL.Host)
						require.Equal(t, "long", gjson.GetBytes(supplier.bodies[i], "commands.response_length").String())
						require.NotEmpty(t, gjson.GetBytes(supplier.bodies[i], "id").String())
						continue
					}
					require.Equal(t, "/basispoints/api/responses", req.URL.Path)
					if i == 0 {
						continue
					}
					rounds := (i + 1) / 2
					input := gjson.GetBytes(supplier.bodies[i], "input").Array()
					require.Len(t, input, len(initialInput)+2*rounds)
					for j, original := range initialInput {
						require.JSONEq(t, original.Raw, input[j].Raw, "preserve complete prepared history and search instructions")
					}
					require.Equal(t, fmt.Sprint(rounds+1), gjson.GetBytes(supplier.bodies[i], "metadata.agent_iteration").String())
					for _, key := range []string{"task_id", "turn_id"} {
						require.Equal(t, gjson.GetBytes(supplier.bodies[0], "metadata."+key).String(), gjson.GetBytes(supplier.bodies[i], "metadata."+key).String())
					}
					for round := 0; round < rounds; round++ {
						call, output := input[len(initialInput)+2*round], input[len(initialInput)+2*round+1]
						require.Equal(t, wantName, call.Get("name").String())
						require.Equal(t, "function_call", call.Get("type").String())
						require.Equal(t, fmt.Sprintf("fc_search_%d", round), call.Get("id").String())
						require.Equal(t, fmt.Sprintf("call_search_%d", round), call.Get("call_id").String())
						require.Equal(t, "function_call_output", output.Get("type").String())
						require.Equal(t, call.Get("call_id").String(), output.Get("call_id").String())
						code := gjson.Get(call.Get("arguments").String(), "code").String()
						require.Equal(t, "__sub2api_web_search", gjson.Get(code, "name").String())
						require.Equal(t, issue871BusinessQuery, gjson.Get(code, "arguments.query").String())
						data := gjson.Get(output.Get("output").String(), "untrusted_search_data")
						require.JSONEq(t, gjson.Get(issue871BusinessResult, "results").Raw, data.Get("results").Raw)
						require.Equal(t, "Synthetic result", data.Get("output").String())
						if strings.HasPrefix(form, "wrapped") {
							exact, marshalErr := json.Marshal(issue871BusinessCall(t, form, round))
							require.NoError(t, marshalErr)
							require.JSONEq(t, string(exact), call.Raw, "genuine native calls must remain verbatim")
						}
					}
				}
				require.NotEqual(t, gjson.GetBytes(supplier.bodies[1], "id").String(), gjson.GetBytes(supplier.bodies[3], "id").String())
				response := gjson.Parse(rec.Body.String())
				if mode == "sse" {
					for _, line := range strings.Split(rec.Body.String(), "\n") {
						if strings.HasPrefix(line, "data: ") {
							event := gjson.Parse(strings.TrimPrefix(line, "data: "))
							if event.Get("type").String() == "response.completed" {
								response = event.Get("response")
							}
						}
					}
				}
				require.Equal(t, "completed", response.Get("status").String())
				require.Equal(t, "resp_business", response.Get("id").String())
				require.Equal(t, int64(30), response.Get("usage.input_tokens").Int())
				require.Equal(t, int64(6), response.Get("usage.output_tokens").Int())
				output := response.Get("output").Array()
				require.Len(t, output, 3)
				for _, item := range output[:2] {
					require.Equal(t, "web_search_call", item.Get("type").String())
					require.Equal(t, issue871BusinessQuery, item.Get("action.query").String())
					require.Equal(t, "https://example.invalid/source", item.Get("action.sources.0.url").String())
				}
				require.NotEqual(t, output[0].Get("id").String(), output[1].Get("id").String())
				require.Equal(t, issue871BusinessAnswer, output[2].Get("content.0.text").String())
				citation := output[2].Get("content.0.annotations.0")
				require.Equal(t, "url_citation", citation.Get("type").String())
				require.Equal(t, "https://example.invalid/source", citation.Get("url").String())
				require.Equal(t, "Synthetic source", citation.Get("title").String())
				require.Equal(t, int64(2), citation.Get("start_index").Int())
				require.Equal(t, int64(len([]rune(issue871BusinessAnswer))), citation.Get("end_index").Int())
				require.NotContains(t, rec.Body.String(), "__sub2api_web_search")
				require.NotContains(t, rec.Body.String(), "run_officejs")
			})
		}
	}
}

func TestExcelBPSSearchBusinessFailuresThroughForward(t *testing.T) {
	for _, tc := range []struct {
		failure, phase, reason string
		sends                  int
	}{
		{"invalid_arguments", "tool_validation", "invalid_tool_response", 1},
		{"unknown_tool", "tool_validation", "invalid_tool_response", 1},
		{"search_transport", "search_http", "transport_failed", 2},
		{"search_403", "search_http", "http_rejected", 2},
		{"search_429", "search_http", "http_rejected", 2},
		{"search_parse", "search_result", "parse_failed", 2},
		{"continuation_403", "model_continuation", "http_rejected", 3},
		{"continuation_eof", "model_continuation", "unexpected_eof", 3},
		{"repeated_call", "tool_validation", "duplicate_call_id", 3},
	} {
		for _, mode := range []string{"json", "sse", "compact_keepalive"} {
			t.Run(tc.failure+"/"+mode, func(t *testing.T) {
				supplier := &issue871BusinessSupplier{form: "direct", failure: tc.failure}
				svc, account, c, rec, result, err := issue871BusinessForward(t, supplier, mode)
				supplier.mu.Lock()
				defer supplier.mu.Unlock()
				require.Error(t, err)
				require.NotNil(t, result)
				require.Len(t, supplier.requests, tc.sends, "no repair, search replay, or extra continuation")
				wantStatus := http.StatusOK
				if mode == "json" {
					wantStatus = http.StatusBadGateway
				}
				require.Equal(t, wantStatus, rec.Code, "preserve an already committed HTTP status")
				require.NotContains(t, rec.Body.String(), "response.completed")
				detail := issue871Error(t, rec.Body.String(), mode != "json")
				require.Equal(t, "basispoints_search_failed", detail.Get("code").String())
				require.Contains(t, detail.Get("message").String(), "phase="+tc.phase+"; reason="+tc.reason)
				ops, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.Equal(t, 502, ops.IntendedStatus)
				for _, private := range []string{"PRIVATE_BODY", "PRIVATE_TOKEN", "PRIVATE_KEY", "PRIVATE_QUERY", issue871BusinessQuery, "test-token", "test-account"} {
					require.NotContains(t, detail.Raw+ops.Message+ops.UpstreamDetail, private)
				}
				require.True(t, account.IsExcelBPSEnabled())
				require.True(t, account.Schedulable)
				require.False(t, svc.isExcelBPSCoolingDown(account, "gpt-6-astra"))
				require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
			})
		}
	}
}
