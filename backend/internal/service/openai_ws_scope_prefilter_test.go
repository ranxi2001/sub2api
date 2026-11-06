//go:build unit

package service

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func BenchmarkExecutionScopeLargePayload(b *testing.B) {
	plain := append([]byte(`{"input":"`), bytes.Repeat([]byte("x"), 8<<20)...)
	plain = append(plain, []byte(`"}`)...)
	escaped := append([]byte(`{"input":"\u0020`), bytes.Repeat([]byte("x"), 8<<20)...)
	escaped = append(escaped, []byte(`"}`)...)
	headers := map[string]string{openAIWSThreadIDHeader: "benchmark-thread", openAIWSTurnMetadataHeader: `{"thread_id":"benchmark-thread","request_kind":"turn"}`}
	for _, tc := range []struct {
		name    string
		body    []byte
		headers map[string]string
	}{
		{"missing_metadata", plain, nil}, {"escaped_fallback", escaped, nil}, {"complete_headers", plain, headers},
	} {
		b.Run(tc.name, func(b *testing.B) {
			ctx := newOpenAIWSExecutionScopeTestContext(tc.headers)
			b.SetBytes(int64(len(tc.body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = resolveOpenAIWSExecutionScope(ctx, tc.body, 21)
			}
		})
	}
}

// The reference retains the original parsing behavior without a prefilter.
func executionThreadReference(c *gin.Context, body []byte) string {
	if c != nil && c.Request != nil {
		if id := strings.TrimSpace(c.GetHeader(openAIWSThreadIDHeader)); id != "" {
			return id
		}
		if id := codexTurnMetadataThreadID(c.GetHeader(openAIWSTurnMetadataHeader)); id != "" {
			return id
		}
		if window := strings.TrimSpace(c.GetHeader(openAIWSWindowIDHeader)); window != "" {
			if id := strings.TrimSpace(strings.SplitN(window, ":", 2)[0]); id != "" {
				return id
			}
		}
	}
	if len(body) == 0 {
		return ""
	}
	if id := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.thread_id").String()); id != "" {
		return id
	}
	return codexTurnMetadataThreadID(gjson.GetBytes(body, "client_metadata."+openAIWSTurnMetadataHeader).String())
}

func executionLaneReference(c *gin.Context, body []byte) string {
	var metadata codexTurnMetadata
	validHeader := false
	if c != nil && c.Request != nil {
		metadata, validHeader = parseCodexTurnMetadata(c.GetHeader(openAIWSTurnMetadataHeader))
	}
	if !validHeader && len(body) > 0 {
		metadata, _ = parseCodexTurnMetadata(gjson.GetBytes(body, "client_metadata."+openAIWSTurnMetadataHeader).String())
	}
	switch metadata.RequestKind {
	case "", openAIWSRequestKindTurn, openAIWSRequestKindPrewarm, openAIWSRequestKindCompaction:
	default:
		return "kind=" + metadata.RequestKind
	}
	if metadata.ThreadID == "" {
		var subagent string
		if c != nil && c.Request != nil {
			subagent = strings.TrimSpace(c.GetHeader(openAISubagentHeader))
		}
		if subagent == "" && len(body) > 0 {
			subagent = strings.TrimSpace(gjson.GetBytes(body, "client_metadata."+openAISubagentHeader).String())
		}
		if subagent != "" {
			return "subagent=" + strings.ToLower(subagent)
		}
	}
	return ""
}

func executionScopeReference(c *gin.Context, body []byte, apiKeyID int64) (string, string) {
	lane := executionLaneReference(c, body)
	if thread := executionThreadReference(c, body); thread != "" {
		scope, _ := deriveOpenAISessionHashes(openAIWSExecutionScopeSeed(apiKeyID, "thread", thread, lane))
		return scope, thread
	}
	if session := strings.TrimSpace(explicitOpenAIRequestSessionID(c, body)); session != "" {
		scope, _ := deriveOpenAISessionHashes(openAIWSExecutionScopeSeed(apiKeyID, "session", session, lane))
		return scope, ""
	}
	return "", ""
}

func TestExecutionScopeMetadataPrefilterParity(t *testing.T) {
	cases := []struct {
		name, body, wantThread string
		headers                map[string]string
	}{
		{"plain session", `{"input":"hello"}`, "", map[string]string{"session-id": "s"}},
		{"body thread", `{"client_metadata":{"thread_id":"child"}}`, "child", nil},
		{"escaped body key", `{"client_\u006detadata":{"thread_id":"child"}}`, "child", nil},
		{"header wins", `{"client_metadata":{"thread_id":"child"}}`, "header", map[string]string{"thread-id": "header"}},
		{"header survives absent body metadata", `{"input":"hello"}`, "header", map[string]string{"thread-id": "header"}},
		{"metadata inside value only", `{"input":"client_metadata"}`, "", nil},
		{"unicode outside metadata", `{"input":"\u0020"}`, "", nil},
		{"malformed", `{"input":"broken`, "", nil},
		{"duplicate metadata", `{"client_metadata":{"thread_id":"first"},"client_metadata":{"thread_id":"second"}}`, "first", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newOpenAIWSExecutionScopeTestContext(tc.headers)
			wantScope, wantThread := executionScopeReference(c, []byte(tc.body), 21)
			gotScope, gotThread := resolveOpenAIWSExecutionScope(c, []byte(tc.body), 21)
			if gotScope != wantScope || gotThread != wantThread || gotThread != tc.wantThread {
				t.Fatalf("execution scope changed: thread=%q expected=%q", gotThread, tc.wantThread)
			}
		})
	}
}

func FuzzExecutionScopeMetadataPrefilterParity(f *testing.F) {
	for _, body := range [][]byte{
		[]byte(`{"input":"hello"}`),
		[]byte(`{"client_metadata":{"thread_id":"child"}}`),
		[]byte(`{"client_\u006detadata":{"thread_id":"child"}}`),
		[]byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"memory\"}"}}`),
		[]byte(`{"prompt_cache_key":"cache-1"}`),
		[]byte(`{"input":"broken`),
	} {
		for mode := uint8(0); mode < 4; mode++ {
			f.Add(body, mode)
		}
	}
	f.Fuzz(func(t *testing.T, body []byte, mode uint8) {
		if len(body) > 1<<20 {
			return
		}
		var c *gin.Context
		switch mode % 4 {
		case 1:
			c = newOpenAIWSExecutionScopeTestContext(map[string]string{"session-id": "session"})
		case 2:
			c = newOpenAIWSExecutionScopeTestContext(map[string]string{"thread-id": "header-thread"})
		case 3:
			c = newOpenAIWSExecutionScopeTestContext(map[string]string{openAIWSTurnMetadataHeader: `{"request_kind":"memory"}`})
		}
		wantScope, wantThread := executionScopeReference(c, body, 21)
		gotScope, gotThread := resolveOpenAIWSExecutionScope(c, body, 21)
		if gotScope != wantScope || gotThread != wantThread {
			t.Fatalf("execution scope changed for mode %d", mode%4)
		}
	})
}
