//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

var affinityContextSink context.Context

func referenceAffinity(ctx context.Context, body []byte) context.Context {
	input := gjson.GetBytes(body, "input")
	found := false
	visit := func(item gjson.Result) {
		if item.Get("type").String() == "reasoning" {
			e := item.Get("encrypted_content")
			found = found || e.Type == gjson.String && e.String() != ""
		}
	}
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool { visit(item); return !found })
	} else if input.IsObject() {
		visit(input)
	}
	return context.WithValue(ctx, openAIEncryptedReasoningAffinityKey{}, found)
}

func assertAffinityParity(t testing.TB, body []byte) {
	t.Helper()
	key := openAIEncryptedReasoningAffinityKey{}
	got := WithOpenAIEncryptedReasoningAffinity(context.Background(), body).Value(key)
	want := referenceAffinity(context.Background(), body).Value(key)
	if got != want {
		t.Fatalf("affinity differs on %d bytes", len(body))
	}
}

func TestAffinityPrefilterParity(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"input":[]}`, `{"input":[{"type":"message","content":"ordinary"}]}`,
		`{"input":{"type":"reasoning","encrypted_content":"synthetic"}}`,
		`{"input":[{"type":"reasoning","encrypted_content":"synthetic"}]}`,
		`{"input":[{"type":"reasoning","encrypted_\u0063ontent":"synthetic"}]}`,
		`{"input":[{"type":"\u0072easoning","encrypted_content":"synthetic"}]}`,
		`{"in\u0070ut":[{"type":"reasoning","encrypted_content":"synthetic"}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":""}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":null}]}`,
		`{"input":[{"type":"message","encrypted_content":"synthetic"}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":"synthetic"}],"input":[]}`,
		`{"input":[],"input":[{"type":"reasoning","encrypted_content":"synthetic"}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":"synthetic"}]`,
	} {
		assertAffinityParity(t, []byte(body))
	}
}

func TestAffinityPrefilterAvoidsInputAllocation(t *testing.T) {
	body := append([]byte(`{"input":[{"type":"message","content":"`), bytes.Repeat([]byte("x"), 256<<10)...)
	body = append(body, `"}]}`...)
	ctx := context.Background()
	key := openAIEncryptedReasoningAffinityKey{}
	minimum := testing.AllocsPerRun(10, func() { affinityContextSink = context.WithValue(ctx, key, false) })
	got := testing.AllocsPerRun(10, func() { affinityContextSink = WithOpenAIEncryptedReasoningAffinity(ctx, body) })
	if got > minimum {
		t.Fatalf("ordinary input uses %.0f allocations; context-only uses %.0f", got, minimum)
	}
}

func TestAffinityPrefilterEscapedKeyPositions(t *testing.T) {
	for _, name := range []string{"input", "type", "encrypted_content"} {
		for i := range len(name) {
			escaped := name[:i] + fmt.Sprintf(`\u%04x`, name[i]) + name[i+1:]
			keys := map[string]string{"input": "input", "type": "type", "encrypted_content": "encrypted_content"}
			keys[name] = escaped
			body := []byte(fmt.Sprintf(`{"%s":[{"%s":"reasoning","%s":"synthetic"}]}`, keys["input"], keys["type"], keys["encrypted_content"]))
			assertAffinityParity(t, body)
			if WithOpenAIEncryptedReasoningAffinity(context.Background(), body).Value(openAIEncryptedReasoningAffinityKey{}) != true {
				t.Fatalf("escaped key %s at position %d lost the encrypted affinity", name, i)
			}
		}
	}
}

func FuzzAffinityPrefilterParity(f *testing.F) {
	for _, seed := range []string{
		`{}`, `{"input":{"type":"reasoning","encrypted_content":"synthetic"}}`,
		`{"input":[{"type":"reasoning","encrypted_\u0063ontent":"synthetic"}]}`, `{"input":[`, `[]`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 8192 {
			return
		}
		assertAffinityParity(t, body)
	})
}

func BenchmarkAffinityPrefilter(b *testing.B) {
	for _, tc := range []struct{ name, prefix, suffix string }{
		{"ordinary", `{"input":[{"type":"message","content":"`, `"}]}`},
		{"escaped", `{"unicode":"\u4e2d","input":[{"type":"message","content":"`, `"}]}`},
		{"encrypted", `{"input":[{"type":"reasoning","encrypted_content":"synthetic"},{"type":"message","content":"`, `"}]}`},
	} {
		b.Run(tc.name, func(b *testing.B) {
			body := append([]byte(tc.prefix), bytes.Repeat([]byte("x"), 4<<20)...)
			body = append(body, tc.suffix...)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				affinityContextSink = WithOpenAIEncryptedReasoningAffinity(context.Background(), body)
			}
		})
	}
}
