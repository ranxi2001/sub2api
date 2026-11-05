package service

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// This fixture contains no real messages or network access.
func syntheticOAuthMessageHistory() []byte {
	var body bytes.Buffer
	_, _ = body.WriteString(`{"model":"gpt-5.4","input":[`)
	content := strings.Repeat("synthetic ", 128)
	for i := 0; i < 2000; i++ {
		if i != 0 {
			_ = body.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&body, `{"type":"message","id":"msg_%06d","role":"user","content":[{"type":"input_text","text":%q}]}`, i, content)
	}
	_, _ = body.WriteString(`]}`)
	return body.Bytes()
}

func TestOAuthCompatibilityMessageHistoryAvoidsRedundantDecode(t *testing.T) {
	request := syntheticOAuthMessageHistory()
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	allocations := testing.AllocsPerRun(2, func() {
		if _, _, err := normalizeOpenAIResponsesWebSocketCompatibilityBody(request, account, false); err != nil {
			t.Fatal(err)
		}
	})
	// The previous full map decode adds tens of thousands of allocations for
	// this history even though no tool output can be removed.
	if allocations > 90000 {
		t.Fatalf("message-only history allocated %.0f times; maximum 90000", allocations)
	}
}

// Exercise a large message-only Responses history through OAuth compatibility.
func BenchmarkOAuthResponsesCompatibilityMessageHistory(b *testing.B) {
	request := syntheticOAuthMessageHistory()
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	b.SetBytes(int64(len(request)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := normalizeOpenAIResponsesWebSocketCompatibilityBody(request, account, false); err != nil {
			b.Fatal(err)
		}
	}
}
