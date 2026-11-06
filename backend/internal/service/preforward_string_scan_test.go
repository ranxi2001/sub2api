package service

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"testing"
)

// Benchmark the long-string case without making tests depend on machine load.
func BenchmarkLongJSONStringScan(b *testing.B) {
	body := append(append([]byte{'"'}, bytes.Repeat([]byte("x"), 8<<20)...), '"')
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := openAIResponsesToolSchemaParser{body: body}
		_, end, err := p.parseString()
		if err != nil {
			b.Fatal(err)
		}
		benchmarkIntSink = end
	}
}

func TestJSONStringScannerPreservesValidation(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		valid       bool
	}{
		{"escaped quote", `"a\\\"b"`, true},
		{"unicode escape", `"a\u0028b"`, true},
		{"escaped slash", `"a\/b"`, true},
		{"invalid escape", `"a\qb"`, false},
		{"short unicode", `"a\u12"`, false},
		{"control byte", "\"abc\x01def\"", false},
		{"unterminated", `"abc`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := openAIResponsesToolSchemaParser{body: []byte(tc.input)}
			_, end, err := p.parseString()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if tc.valid && end != len(tc.input) {
				t.Fatalf("end=%d want=%d", end, len(tc.input))
			}
		})
	}
}

func TestJSONStringScannerMatchesJSONForGeneratedStrings(t *testing.T) {
	rng := rand.New(rand.NewSource(1753))
	alphabet := []rune("ASCII中文😊\\\"\n\t\x00")
	for i := 0; i < 500; i++ {
		runes := make([]rune, rng.Intn(1024))
		for j := range runes {
			runes[j] = alphabet[rng.Intn(len(alphabet))]
		}
		body, err := json.Marshal(string(runes))
		if err != nil {
			t.Fatal(err)
		}
		p := openAIResponsesToolSchemaParser{body: body}
		start, end, err := p.parseString()
		if err != nil || start != 0 || end != len(body) {
			t.Fatalf("generated case %d failed: start=%d end=%d err=%v", i, start, end, err)
		}
	}
}
