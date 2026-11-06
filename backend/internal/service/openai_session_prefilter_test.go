//go:build unit

package service

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func BenchmarkExplicitSessionLargePayload(b *testing.B) {
	plain := append([]byte(`{"input":"`), bytes.Repeat([]byte("x"), 8<<20)...)
	plain = append(plain, []byte(`"}`)...)
	escaped := append([]byte(`{"input":"\u0020`), bytes.Repeat([]byte("x"), 8<<20)...)
	escaped = append(escaped, []byte(`"}`)...)
	for _, tc := range []struct {
		name string
		body []byte
	}{{"missing_key", plain}, {"escaped_fallback", escaped}} {
		b.Run(tc.name, func(b *testing.B) {
			ctx := newOpenAIWSExecutionScopeTestContext(nil)
			b.SetBytes(int64(len(tc.body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = explicitOpenAIRequestSessionID(ctx, tc.body)
			}
		})
	}
}

func explicitSessionReference(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}
	session := explicitOpenAIHeaderSessionID(c)
	if session == "" && isGrokRequestContext(c) {
		session = strings.TrimSpace(c.GetHeader(grokConversationIDHeader))
	}
	if session == "" && len(body) > 0 {
		session = strings.TrimSpace(openAIRequestPayloadView(body).Get("prompt_cache_key").String())
	}
	if session == "" && isGrokRequestContext(c) && len(body) > 0 {
		session = grokPreviousResponseSessionSeed(body)
	}
	return session
}

func TestExplicitSessionCacheKeyPrefilterParity(t *testing.T) {
	cases := []struct {
		name, body, want string
		grok             bool
		headers          map[string]string
	}{
		{"absent", `{"input":"hello"}`, "", false, nil},
		{"cache key", `{"prompt_cache_key":"cache-1"}`, "cache-1", false, nil},
		{"escaped cache key", `{"prompt_cache_\u006bey":"cache-1"}`, "cache-1", false, nil},
		{"nested WS envelope", `{"type":"response.create","response":{"prompt_cache_key":"nested"}}`, "nested", false, nil},
		{"header wins", `{"prompt_cache_key":"body"}`, "header", false, map[string]string{"session-id": "header"}},
		{"value only", `{"input":"prompt_cache_key"}`, "", false, nil},
		{"Grok fallback", `{"previous_response_id":"resp_123"}`, "grok-prev-resp:resp_123", true, nil},
		{"Grok header wins", `{"previous_response_id":"resp_123"}`, "grok-header", true, map[string]string{grokConversationIDHeader: "grok-header"}},
		{"malformed", `{"input":"broken`, "", false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newOpenAIWSExecutionScopeTestContext(tc.headers)
			if tc.grok {
				c.Set("api_key", &APIKey{Group: &Group{Platform: PlatformGrok}})
			}
			got := explicitOpenAIRequestSessionID(c, []byte(tc.body))
			if reference := explicitSessionReference(c, []byte(tc.body)); got != reference || got != tc.want {
				t.Fatalf("session identity changed: got %q want %q reference %q", got, tc.want, reference)
			}
		})
	}
}

func FuzzExplicitSessionCacheKeyPrefilterParity(f *testing.F) {
	for _, body := range [][]byte{
		[]byte(`{"input":"hello"}`),
		[]byte(`{"prompt_cache_key":"cache-1"}`),
		[]byte(`{"prompt_cache_\u006bey":"cache-1"}`),
		[]byte(`{"type":"response.create","response":{"prompt_cache_key":"nested"}}`),
		[]byte(`{"previous_response_id":"resp_123"}`),
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
			c = newOpenAIWSExecutionScopeTestContext(nil)
		case 2:
			c = newOpenAIWSExecutionScopeTestContext(map[string]string{"session-id": "header"})
		case 3:
			c = newOpenAIWSExecutionScopeTestContext(nil)
			c.Set("api_key", &APIKey{Group: &Group{Platform: PlatformGrok}})
		}
		if got, want := explicitOpenAIRequestSessionID(c, body), explicitSessionReference(c, body); got != want {
			t.Fatalf("session identity changed for mode %d", mode%4)
		}
	})
}
