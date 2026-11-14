package basispoints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func prepareSearch(t *testing.T, source object, automatic bool, cache *CatalogCache) ([]byte, *Bridge, *SearchPlan) {
	t.Helper()
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	normalized, plan, err := NormalizeWebSearchRequest(raw, true, automatic)
	require.NoError(t, err)
	wire, bridge, err := PrepareWithCatalog(normalized, "search-scope", nil, cache)
	require.NoError(t, err)
	wire, err = plan.Attach(wire, bridge)
	require.NoError(t, err)
	return wire, bridge, plan
}
func searchSource() object {
	s := testSource()
	s["stream"] = true
	s["tools"] = []any{object{"type": "web_search"}}
	return s
}
func searchCall(index int) object {
	call := nativeCall(object{"name": serverSearchTool, "arguments": object{"query": fmt.Sprintf("query %d", index)}})
	call["id"] = fmt.Sprintf("fc_%d", index)
	call["call_id"] = fmt.Sprintf("call_%d", index)
	return call
}
func searchRound(index int, items ...any) io.ReadCloser {
	return io.NopCloser(strings.NewReader(sse(object{"type": "response.completed", "response": repairResponse(fmt.Sprintf("resp_%d", index), 10, 2, items...)})))
}
func searchAnswer(id, value string) object {
	return object{"type": "message", "id": id, "role": "assistant", "content": []any{object{"type": "output_text", "text": value}}}
}

func TestWebSearchOptionsRespectClientContract(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tool      object
		choice    any
		wantPlan  bool
		wantError string
	}{
		{"explicit", object{"type": "web_search"}, nil, true, ""},
		{"preview", object{"type": "web_search_preview", "search_context_size": "high"}, nil, true, ""},
		{"none", object{"type": "web_search"}, "none", false, ""},
		{"cache-only", object{"type": "web_search", "external_web_access": false}, nil, false, ""},
		{"forced-cache", object{"type": "web_search", "external_web_access": false}, object{"type": "web_search"}, false, "cache_only_required"},
		{"domains", object{"type": "web_search", "filters": object{"allowed_domains": []any{"example.com"}}}, nil, false, "domain_filter"},
		{"location", object{"type": "web_search", "user_location": object{"country": "US"}}, nil, false, "user_location"},
		{"images", object{"type": "web_search", "search_content_types": []any{"image"}}, nil, false, "image_search"},
		{"mixed-content", object{"type": "web_search", "search_content_types": []any{"text", "image"}}, nil, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := testSource()
			source["tools"] = []any{tc.tool}
			source["tool_choice"] = tc.choice
			raw, _ := json.Marshal(source)
			body, plan, err := NormalizeWebSearchRequest(raw, true, true)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantPlan, plan != nil)
			if tc.name == "cache-only" {
				require.Contains(t, string(body), "Cache-only web search is unavailable")
			}
		})
	}
}

func TestWebSearchAutoDoesNotPolluteCatalogOrOverrideSelection(t *testing.T) {
	cache := new(CatalogCache)
	source := testSource()
	source["tools"] = choiceTools()
	_, bridge, plan := prepareSearch(t, source, true, cache)
	require.NotNil(t, plan)
	require.Contains(t, bridge.tools, serverSearchTool)
	stored, _ := cache.snapshot("search-scope")
	require.NotContains(t, string(stored), serverSearchTool)
	for _, choice := range []any{"none", "required", object{"type": "function", "name": "lookup"}} {
		source["tool_choice"] = choice
		raw, _ := json.Marshal(source)
		_, plan, err := NormalizeWebSearchRequest(raw, true, true)
		require.NoError(t, err)
		require.Nil(t, plan)
	}
	raw, _ := json.Marshal(searchSource())
	same, plan, err := NormalizeWebSearchRequest(raw, false, true)
	require.NoError(t, err)
	require.Equal(t, raw, same)
	require.Nil(t, plan)
}

func TestWebSearchEightRoundsPreserveAllResultsAndUsage(t *testing.T) {
	wire, bridge, plan := prepareSearch(t, searchSource(), false, nil)
	searches, continuations := 0, 0
	stream := bridge.StreamWithSearch(context.Background(), searchRound(0, searchCall(0)), wire, plan, func(_ context.Context, query string) (SearchResult, error) {
		require.Equal(t, fmt.Sprintf("query %d", searches), query)
		searches++
		return SearchResult{Output: "result", Results: []object{}, Sources: []object{{"url": "https://example.com", "title": "Example"}}}, nil
	}, func(_ context.Context, body []byte) (io.ReadCloser, error) {
		continuations++
		require.Contains(t, string(body), "untrusted_search_data")
		require.NotContains(t, string(body), `"type":"web_search_call"`)
		if continuations < 8 {
			return searchRound(continuations, searchCall(continuations)), nil
		}
		return searchRound(continuations, searchAnswer("answer", "See https://example.com")), nil
	})
	events := repairEvents(t, stream)
	require.Equal(t, 8, searches)
	require.Equal(t, 8, continuations)
	last := events[len(events)-1]
	require.Equal(t, "response.completed", last["type"])
	response := repairValue[object](t, last["response"])
	require.Equal(t, "resp_0", response["id"])
	require.Equal(t, "90", fmt.Sprint(repairValue[object](t, response["usage"])["input_tokens"]))
	output := repairValue[[]any](t, response["output"])
	require.Len(t, output, 9)
	for _, item := range output[:8] {
		require.Equal(t, "web_search_call", repairValue[object](t, item)["type"])
	}
	encoded, _ := json.Marshal(last)
	require.Contains(t, string(encoded), "url_citation")
	require.NotContains(t, string(encoded), serverSearchTool)
}

func TestWebSearchRejectsMixedAndRepeatedCallsBeforeExecution(t *testing.T) {
	for _, mode := range []string{"mixed", "repeated", "missing", "backend-error"} {
		t.Run(mode, func(t *testing.T) {
			source := searchSource()
			source["tools"] = append(source["tools"].([]any), object{"type": "function", "name": "other"})
			if mode == "missing" {
				source["tool_choice"] = object{"type": "web_search"}
			}
			wire, bridge, plan := prepareSearch(t, source, false, nil)
			items := []any{searchCall(0)}
			if mode == "mixed" {
				call := repairCall("other", "Call", `{"name":"other","arguments":{}}`)
				items = append(items, call)
			}
			if mode == "missing" {
				items = []any{searchAnswer("answer", "No search")}
			}
			searches, nexts := 0, 0
			stream := bridge.StreamWithSearch(context.Background(), searchRound(0, items...), wire, plan, func(context.Context, string) (SearchResult, error) {
				searches++
				if mode == "backend-error" {
					return SearchResult{}, errors.New("synthetic backend rejection")
				}
				return SearchResult{Results: []object{}}, nil
			}, func(context.Context, []byte) (io.ReadCloser, error) {
				nexts++
				return searchRound(1, searchCall(0)), nil
			})
			events := repairEvents(t, stream)
			require.Equal(t, "response.failed", events[len(events)-1]["type"])
			if mode == "mixed" || mode == "missing" {
				require.Zero(t, searches)
			} else {
				require.Equal(t, 1, searches)
			}
			if mode == "backend-error" {
				require.Zero(t, nexts)
			}
		})
	}
}

func TestWebSearchTextStreamsBeforeTerminalAndCancelStopsSearch(t *testing.T) {
	wire, bridge, plan := prepareSearch(t, searchSource(), false, nil)
	reader, writer := io.Pipe()
	stream := bridge.StreamWithSearch(context.Background(), reader, wire, plan, nil, nil)
	sent := make(chan struct{})
	go func() {
		_, _ = io.WriteString(writer, sse(object{"type": "response.output_item.added", "output_index": 0, "item": object{"type": "message", "id": "msg", "role": "assistant", "content": []any{}}})+sse(object{"type": "response.output_text.delta", "output_index": 0, "item_id": "msg", "content_index": 0, "delta": "early"}))
		close(sent)
	}()
	seen := make(chan struct{})
	go func() {
		_ = readEvents(stream, func(_ string, data []byte) error {
			if strings.Contains(string(data), "early") {
				close(seen)
				return io.EOF
			}
			return nil
		})
	}()
	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("text was buffered until terminal")
	}
	require.NoError(t, stream.Close())
	_ = writer.Close()
	<-sent
	var cancelled atomic.Bool
	started := make(chan struct{})
	stream = bridge.StreamWithSearch(context.Background(), searchRound(0, searchCall(0)), wire, plan, func(ctx context.Context, _ string) (SearchResult, error) {
		close(started)
		<-ctx.Done()
		cancelled.Store(true)
		return SearchResult{}, ctx.Err()
	}, func(context.Context, []byte) (io.ReadCloser, error) { return nil, nil })
	drained := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, stream); close(drained) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("search never started")
	}
	_ = stream.Close()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not close stream")
	}
	require.Eventually(t, cancelled.Load, time.Second, time.Millisecond)
}

func TestAlphaSearchResultRejectsUnverifiedAndMalformedResponses(t *testing.T) {
	for _, raw := range []string{`{"output":"claimed result"}`, `{"results":null}`, `{"results":{},"output":"text"}`, `{"results":[],"error":{"message":"private-body"}}`, strings.Repeat("x", maxSearchResultBytes+1)} {
		_, err := ParseSearchResult([]byte(raw))
		require.Error(t, err)
		require.NotContains(t, err.Error(), "private-body")
	}
	result, err := ParseSearchResult([]byte(`{"output":"text","results":[{"url":"https://example.com","title":"Example"},{"url":"javascript:alert(1)"}]}`))
	require.NoError(t, err)
	require.Len(t, result.Sources, 1)
}
