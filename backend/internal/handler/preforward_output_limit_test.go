package handler

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Reference behavior is kept here to catch accidental changes to billing estimates.
func referenceRequestMaxOutputTokens(body []byte) int {
	for _, path := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens", "generationConfig.maxOutputTokens", "generation_config.max_output_tokens"} {
		if v := gjson.GetBytes(body, path); v.Exists() && v.Type == gjson.Number && v.Int() > 0 {
			return int(v.Int())
		}
	}
	return 0
}

func TestOutputLimitFastPathCoversEverySupportedPath(t *testing.T) {
	for _, path := range inflightOutputLimitPaths {
		if !strings.Contains(path, "max") {
			t.Fatalf("output limit path %q is not covered by the fast path", path)
		}
	}
}

func TestRequestMaxOutputTokensEscapedFieldNames(t *testing.T) {
	cases := []struct {
		field, template string
	}{
		{"max_tokens", `{"%s":37}`},
		{"max_completion_tokens", `{"%s":37}`},
		{"max_output_tokens", `{"%s":37}`},
		{"maxOutputTokens", `{"generationConfig":{"%s":37}}`},
		{"max_output_tokens", `{"generation_config":{"%s":37}}`},
	}
	for _, tc := range cases {
		for i := range len(tc.field) {
			escaped := tc.field[:i] + fmt.Sprintf("\\u%04x", tc.field[i]) + tc.field[i+1:]
			body := []byte(fmt.Sprintf(tc.template, escaped))
			if got := requestMaxOutputTokens(body); got != 37 {
				t.Fatalf("field %q escaped at %d: got %d, want 37", tc.field, i, got)
			}
		}
	}
}

func TestRequestMaxOutputTokensMatchesReference(t *testing.T) {
	cases := []string{
		`{}`,
		`{"input":"ordinary long history"}`,
		`{"max_tokens":8,"max_output_tokens":9}`,
		`{"max_tokens":-1,"max_completion_tokens":12}`,
		`{"max_tokens":"3","max_tokens":9,"max_output_tokens":7}`,
		`{"generationConfig":{"maxOutputTokens":17}}`,
		`{"generation_config":{"max_output_tokens":18}}`,
		`{"\u006d\u0061\u0078_tokens":19}`,
		`{"ma\u0078_output_tokens":20}`,
		`{"generationConfig":{"\u006daxOutputTokens":21}}`,
		`{"input":"\\u006d","max_output_tokens":22}`,
		`{"max_tokens":3`,
		`{"max_tokens\q":23}`,
		`{"max_tokens":1e100}`,
	}
	for _, body := range cases {
		if got, want := requestMaxOutputTokens([]byte(body)), referenceRequestMaxOutputTokens([]byte(body)); got != want {
			t.Fatalf("body %q: got %d, want %d", body, got, want)
		}
	}
}

func FuzzRequestMaxOutputTokensMatchesReference(f *testing.F) {
	for _, body := range []string{
		`{}`, `{"input":"text"}`, `{"max_tokens":8}`,
		`{"\u006dax_tokens":9}`, `{"generationConfig":{"maxOutputTokens":7}}`,
		`{"max_tokens":0,"max_output_tokens":4}`, `{"max_tokens\q":3}`,
	} {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if got, want := requestMaxOutputTokens(body), referenceRequestMaxOutputTokens(body); got != want {
			t.Fatalf("got %d, want %d", got, want)
		}
	})
}

func BenchmarkRequestMaxOutputTokensWithoutLimit(b *testing.B) {
	body := []byte(`{"input":"` + strings.Repeat("x", 8<<20) + `"}`)
	for _, tc := range []struct {
		name string
		fn   func([]byte) int
	}{
		{"current", requestMaxOutputTokens},
		{"reference", referenceRequestMaxOutputTokens},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			for i := 0; i < b.N; i++ {
				if tc.fn(body) != 0 {
					b.Fatal("unexpected output limit")
				}
			}
		})
	}
}
