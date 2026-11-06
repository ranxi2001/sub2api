package service

import (
	"bytes"
	"testing"
)

func TestSanitizeResponsesInputIDsWithoutKeysDoesNotAllocate(t *testing.T) {
	body := append([]byte(`{"input":[{"type":"message","content":"`), bytes.Repeat([]byte{'a'}, 1<<20)...)
	body = append(body, []byte(`"}]}`)...)
	allocs := testing.AllocsPerRun(5, func() {
		got, changed, err := sanitizeOpenAIResponsesInputItemIDs(body)
		if err != nil || changed || !bytes.Equal(got, body) {
			t.Fatalf("no-ID input changed: changed=%v error=%v", changed, err)
		}
	})
	if allocs != 0 {
		t.Fatalf("no-ID input: got %.1f allocations, want none", allocs)
	}
}

func TestSanitizeResponsesInputIDsEscapedKeyUsesExistingPath(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","\u0069d":"bad"}]}`)
	got, changed, err := sanitizeOpenAIResponsesInputItemIDs(body)
	if err != nil || !changed || bytes.Contains(got, []byte("bad")) {
		t.Fatalf("escaped ID not stripped: changed=%v error=%v", changed, err)
	}
}

func BenchmarkSanitizeResponsesInputIDsWithoutKeys(b *testing.B) {
	body := append([]byte(`{"input":[{"type":"message","content":"`), bytes.Repeat([]byte{'a'}, 4<<20)...)
	body = append(body, []byte(`"}]}`)...)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, changed, err := sanitizeOpenAIResponsesInputItemIDs(body)
		if err != nil || changed || len(got) != len(body) {
			b.Fatalf("no-ID input changed: changed=%v error=%v", changed, err)
		}
	}
}
