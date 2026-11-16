package basispoints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

type WebSearchFunc func(context.Context, string) (SearchResult, error)
type SearchContinuationFunc func(context.Context, []byte) (io.ReadCloser, error)

// StreamWithSearch keeps one downstream response while executing unexposed
// server search calls. It uses the regular bridge for final client tool/schema
// validation. Search turns cannot enter generic tool repair or I/O replay.
func (b *Bridge) StreamWithSearch(parent context.Context, upstream io.ReadCloser, wire []byte, plan *SearchPlan, search WebSearchFunc, next SearchContinuationFunc) io.ReadCloser {
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	reader, writer := io.Pipe()
	upstream = &searchRoundBody{ReadCloser: upstream}
	raw := &streamBody{PipeReader: reader, upstream: upstream, cancel: cancel}
	final := *b
	final.searchDiagnostics = true
	final.tools = make(map[string]tool, len(b.tools))
	for key, value := range b.tools {
		if key != serverSearchTool {
			final.tools[key] = value
		}
	}
	go func() {
		stop := context.AfterFunc(ctx, func() { _ = writer.CloseWithError(ctx.Err()); _ = raw.closeUpstream() })
		defer stop()
		err := b.runWebSearch(ctx, upstream, writer, wire, plan, search, next)
		_ = raw.closeUpstream()
		_ = writer.CloseWithError(err)
	}()
	return final.StreamWithRepairs(parent, raw, nil, nil)
}

type searchOutputStream struct {
	emit            func(string, object) error
	output          []any
	usage           object
	id              string
	model           string
	completedSearch bool
	identities      map[string]bool
	callIDs         map[string]bool
}

func (b *Bridge) runWebSearch(ctx context.Context, upstream io.ReadCloser, writer io.Writer, wire []byte, plan *SearchPlan, search WebSearchFunc, next SearchContinuationFunc) error {
	state := &searchOutputStream{identities: map[string]bool{}, callIDs: map[string]bool{}}
	state.emit = func(kind string, payload object) error {
		payload["type"] = kind
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", kind, raw)
		return err
	}
	err := b.searchRounds(ctx, upstream, wire, plan, search, next, state)
	if err == nil || ctx.Err() != nil || errors.Is(err, io.ErrClosedPipe) {
		return err
	}
	// A search/model continuation failure cannot look like a successful HTTP 200.
	// Keep observed model usage; upstream bodies and search queries are not errors.
	failure := NewSearchFailure("unknown", "unknown", 0)
	var known *SearchFailure
	if errors.As(err, &known) {
		failure = NewSearchFailure(known.phase, known.reason, known.upstreamStatus)
	}
	response := object{"id": state.id, "model": state.model, "status": "failed", "output": []any{}, "usage": state.usage, "error": object{"code": "basispoints_search_failed", "search_diagnostic": failure.details()}}
	return state.emit("response.failed", object{"response": response})
}

func (b *Bridge) searchRounds(ctx context.Context, upstream io.ReadCloser, wire []byte, plan *SearchPlan, search WebSearchFunc, next SearchContinuationFunc, state *searchOutputStream) error {
	var prepared object
	if err := decode(wire, &prepared); err != nil {
		_ = upstream.Close()
		return NewSearchFailure("model_initial", "invalid_continuation", 0)
	}
	current := upstream
	phase := "model_initial"
	defer func() {
		if current != nil {
			_ = current.Close()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		offset := len(state.output)
		bufferText := b.structured != nil || (plan.Required && !state.completedSearch)
		observed := newSearchTextObserver(state, offset, bufferText)
		roundBody := current
		stop := context.AfterFunc(ctx, func() { _ = roundBody.Close() })
		response, err := readSearchRound(current, observed.observe)
		stop()
		_ = current.Close()
		current = nil
		if response != nil {
			state.usage = addRepairUsage(state.usage, response["usage"])
		}
		if state.id == "" {
			state.id = text(response["id"])
			state.model = text(response["model"])
		}
		// Publish billed generation usage before waiting for search/continuation,
		// so a later client cancellation does not erase completed rounds.
		if state.id != "" && state.usage != nil {
			if emitErr := state.emit("response.in_progress", object{"response": object{"id": state.id, "model": state.model, "status": "in_progress", "output": []any{}, "usage": state.usage}}); emitErr != nil {
				return emitErr
			}
		}
		if err != nil {
			return searchFailureAt(err, phase, "read_failed")
		}
		if state.id == "" {
			return NewSearchFailure(phase, "missing_response_id", 0)
		}
		if err = observed.validate(response); err != nil {
			return searchFailureAt(err, phase, "unknown")
		}
		output, _ := response["output"].([]any)
		nativeSearch := []object{}
		searchIndexes := []int{}
		queries := []string{}
		clientCalls := 0
		check := *b
		check.replay = nil
		check.stagedReplays = nil
		// Validate every tool before any search side effect. All IDs are request-wide.
		if err = check.validateToolResponse(response); err != nil {
			return searchFailureAt(err, "tool_validation", "invalid_tool_response")
		}
		for i, rawItem := range output {
			item, _ := rawItem.(object)
			if id := text(item["id"]); id != "" {
				if state.identities[id] {
					return NewSearchFailure("tool_validation", "duplicate_item_id", 0)
				}
				state.identities[id] = true
			}
			if !isTool(item) {
				continue
			}
			callID := text(item["call_id"])
			if callID == "" || state.callIDs[callID] {
				return NewSearchFailure("tool_validation", "duplicate_call_id", 0)
			}
			state.callIDs[callID] = true
			translated, translateErr := check.translateCall(item)
			if translateErr != nil {
				return searchFailureAt(translateErr, "tool_validation", "invalid_tool_response")
			}
			if text(translated["name"]) != serverSearchTool {
				clientCalls++
				continue
			}
			var args object
			if decode([]byte(text(translated["arguments"])), &args) != nil {
				return NewSearchFailure("tool_validation", "invalid_query", 0)
			}
			query := strings.TrimSpace(text(args["query"]))
			if query == "" || len(query) > 4096 {
				return NewSearchFailure("tool_validation", "invalid_query", 0)
			}
			// Direct catalog calls are accepted by translateCall, but their
			// private names are not BPS-native tools. Use the same history
			// wrapper as ordinary direct client calls before continuing.
			// Genuine run_officejs calls retain their original wire fields.
			name := text(item["name"])
			if name != "run_officejs" && name != "functions.run_officejs" {
				item, translateErr = check.rebuildNativeHistoryCall(translated)
				if translateErr != nil {
					return searchFailureAt(translateErr, "tool_validation", "invalid_tool_response")
				}
			}
			nativeSearch = append(nativeSearch, item)
			searchIndexes = append(searchIndexes, i)
			queries = append(queries, query)
		}
		if len(nativeSearch) > 1 {
			return NewSearchFailure("tool_validation", "parallel_search_unavailable", 0)
		}
		if len(nativeSearch) > 0 && clientCalls > 0 {
			return NewSearchFailure("tool_validation", "mixed_tool_batch", 0)
		}
		if len(nativeSearch) == 0 {
			if plan.Required && !state.completedSearch {
				return NewSearchFailure("tool_validation", "required_search_missing", 0)
			}
			if plan.RequiredAny && !state.completedSearch && clientCalls == 0 {
				return NewSearchFailure("tool_validation", "required_tool_missing", 0)
			}
			state.output = append(state.output, output...)
			response["output"], response["usage"], response["id"] = state.output, state.usage, state.id
			if b.structured == nil && bufferText {
				if err = observed.flush(output, offset); err != nil {
					return err
				}
			}
			annotateSearchCitations(state.output)
			return state.emit("response.completed", object{"response": response})
		}
		if search == nil || next == nil {
			return NewSearchFailure("tool_validation", "executor_unavailable", 0)
		}
		index := searchIndexes[0]
		nativeOutput := append([]any(nil), output...)
		nativeOutput[index] = nativeSearch[0]
		// Intermediate prose is not the final structured answer. It remains only
		// in the native continuation and never enters the validated final JSON.
		if b.structured != nil {
			kept := make([]any, 0, len(output))
			newIndex := 0
			for i, rawItem := range output {
				item, _ := rawItem.(object)
				if text(item["type"]) == "message" {
					continue
				}
				if i == index {
					newIndex = len(kept)
				}
				kept = append(kept, rawItem)
			}
			output = kept
			index = newIndex
		}
		call := nativeSearch[0]
		publicID := "ws_" + fingerprint([]any{state.id, text(call["call_id"])})
		action := object{"type": "search", "query": queries[0]}
		public := object{"id": publicID, "type": "web_search_call", "status": "in_progress", "action": action}
		if err = state.emit("response.output_item.added", object{"item": public, "output_index": offset + index}); err != nil {
			return err
		}
		if err = state.emit("response.web_search_call.in_progress", object{"item_id": publicID, "output_index": offset + index}); err != nil {
			return err
		}
		if err = state.emit("response.web_search_call.searching", object{"item_id": publicID, "output_index": offset + index}); err != nil {
			return err
		}
		searchCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		result, searchErr := waitForWebSearch(searchCtx, search, queries[0])
		cancel()
		if searchErr != nil {
			return searchFailureAt(searchErr, "search_http", "unknown")
		}
		state.completedSearch = true
		public["status"] = "completed"
		action["sources"] = result.Sources
		output[index] = public
		if err = state.emit("response.web_search_call.completed", object{"item_id": publicID, "output_index": offset + index}); err != nil {
			return err
		}
		if err = state.emit("response.output_item.done", object{"item": public, "output_index": offset + index}); err != nil {
			return err
		}
		if bufferText && b.structured == nil {
			if err = observed.flush(output, offset); err != nil {
				return err
			}
		}
		state.output = append(state.output, output...)
		resultJSON, marshalErr := json.Marshal(object{"untrusted_search_data": result})
		if marshalErr != nil {
			return marshalErr
		}
		// Continue with genuine native call identity, never the public hosted item.

		input, _ := prepared["input"].([]any)
		input = append(input, nativeOutput...)
		input = append(input, object{"type": "function_call_output", "call_id": call["call_id"], "output": string(resultJSON)})
		prepared["input"] = input
		if meta, ok := prepared["metadata"].(object); ok {
			n, _ := strconv.Atoi(text(meta["agent_iteration"]))
			meta["agent_iteration"] = strconv.Itoa(n + 1)
		}
		encoded, marshalErr := json.Marshal(prepared)
		if marshalErr != nil {
			return marshalErr
		}
		snapshot, _ := json.Marshal(state.output)
		if len(encoded) > 32<<20 || len(snapshot) > 32<<20 {
			return NewSearchFailure("model_continuation", "continuation_too_large", 0)
		}
		current, err = next(ctx, encoded)
		if err != nil {
			return searchFailureAt(err, "model_continuation", "unknown")
		}
		if current == nil {
			return NewSearchFailure("model_continuation", "missing_continuation", 0)
		}
		phase = "model_continuation"
		current = &searchRoundBody{ReadCloser: current}
	}
}

type searchRoundBody struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (b *searchRoundBody) Close() error {
	b.once.Do(func() { b.err = b.ReadCloser.Close() })
	return b.err
}

func readSearchRound(reader io.Reader, observe func(string, object) error) (object, error) {
	var response, usage object
	var terminalErr error
	terminal := false
	err := readEvents(io.LimitReader(reader, 32<<20), func(event string, raw []byte) error {
		if string(raw) == "[DONE]" {
			return nil
		}
		var payload object
		if decode(raw, &payload) != nil || payload == nil {
			return searchRequestError("invalid_event")
		}
		kind := text(payload["type"])
		if kind == "" {
			kind = event
		}
		if current, ok := payload["response"].(object); ok {
			if value, ok := current["usage"].(object); ok {
				usage = maxReportedUsage(usage, value)
			}
		}
		if value, ok := payload["usage"].(object); ok {
			usage = maxReportedUsage(usage, value)
		}
		switch kind {
		case "response.completed", "response.failed", "response.cancelled", "response.incomplete", "error":
			terminal = true
			response, _ = payload["response"].(object)
			if kind != "response.completed" || response == nil || text(response["status"]) != "completed" {
				terminalErr = searchRequestError("model_did_not_complete")
			}
			return io.EOF
		}
		return observe(kind, payload)
	})
	if response == nil {
		response = object{}
	}
	response["usage"] = maxReportedUsage(usage, objectValue(response["usage"]))
	if err != nil && !errors.Is(err, io.EOF) {
		return response, err
	}
	if !terminal {
		return response, io.ErrUnexpectedEOF
	}
	return response, terminalErr
}

func objectValue(value any) object { result, _ := value.(object); return result }

// Citations are created only for literal source URLs actually present in text.
func annotateSearchCitations(output []any) {
	sources := map[string]object{}
	for _, raw := range output {
		item, _ := raw.(object)
		if text(item["type"]) == "web_search_call" {
			action, _ := item["action"].(object)
			list, _ := action["sources"].([]object)
			for _, source := range list {
				sources[text(source["url"])] = source
			}
		}
	}
	for _, raw := range output {
		item, _ := raw.(object)
		parts, _ := item["content"].([]any)
		for _, partRaw := range parts {
			part, _ := partRaw.(object)
			if text(part["type"]) != "output_text" {
				continue
			}
			value := text(part["text"])
			annotations, _ := part["annotations"].([]any)
			for link, source := range sources {
				if start := strings.Index(value, link); start >= 0 {
					annotations = append(annotations, object{"type": "url_citation", "url": link, "title": source["title"], "start_index": len([]rune(value[:start])), "end_index": len([]rune(value[:start+len(link)]))})
				}
			}
			if len(annotations) > 0 {
				part["annotations"] = annotations
			}
		}
	}
}

// The gateway keeps sending SSE keepalives while this cancellable wait runs.
func waitForWebSearch(ctx context.Context, search WebSearchFunc, query string) (SearchResult, error) {
	type outcome struct {
		result SearchResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() { result, err := search(ctx, query); done <- outcome{result, err} }()
	for {
		select {
		case value := <-done:
			return value.result, value.err
		case <-ctx.Done():
			return SearchResult{}, ctx.Err()
		}
	}
}
