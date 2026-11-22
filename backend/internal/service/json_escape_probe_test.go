//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJSONMayEscapePlainASCII(t *testing.T) {
	// U(hex) spells the six-byte JSON escape backslash, 'u', hex.
	U := func(hex string) string { return `\` + "u" + hex }
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"no escapes", `{"input":[{"type":"message","content":"hi"}]}`, false},
		{"ansi control escape from shell output", `{"output":"` + U("001b") + `[31mred` + U("001b") + `[0m"}`, false},
		{"go html-safe escapes", `{"text":"a ` + U("003c") + ` b ` + U("0026") + ` c ` + U("003e") + ` d"}`, false},
		{"quote apostrophe and backslash escapes", `{"text":"` + U("0022") + U("0027") + U("005c") + `"}`, false},
		{"non-ascii and surrogate pair", `{"text":"caf` + U("00e9") + ` ` + U("d83d") + U("de00") + ` ` + U("4e2d") + `"}`, false},
		{"escaped letter hides a keyword", `{"type":"` + U("0063") + `ompaction_trigger"}`, true},
		{"upper-case hex escaped underscore", `{"type":"input` + U("005F") + `image"}`, true},
		{"escaped digit", `{"n":"` + U("0031") + `"}`, true},
		{"escaped backslash before u is literal text", `{"code":"\` + U("0041") + `"}`, false},
		{"escaped backslash in a path", `{"cwd":"C:\\users\\dev","re":"\\u{1F600}"}`, false},
		{"escape after an escaped backslash", `{"t":"\\` + U("0063") + `"}`, true},
		{"truncated escape", `{"t":"` + U("00"), true},
		{"invalid hex", `{"t":"` + U("ZZZZ") + `"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, JSONMayEscapePlainASCII([]byte(tc.body)))
		})
	}
}

func TestJSONMayEscapePlainASCIIBackslashRuns(t *testing.T) {
	for count := 0; count <= 9; count++ {
		run := strings.Repeat("\\", count)
		odd := count%2 != 0
		for _, prefix := range []string{"", "\\u003c", "\\n" + strings.Repeat("x", 15), "\\n" + strings.Repeat("x", 16), strings.Repeat("code\\n", 100)} {
			require.Equal(t, odd, JSONMayEscapePlainASCII([]byte(prefix+run)), "trailing run %d", count)
			require.Equal(t, odd, JSONMayEscapePlainASCII([]byte(prefix+run+"u0061")), "keyword run %d", count)
			require.Equal(t, odd, JSONMayEscapePlainASCII([]byte(prefix+run+"u0x")), "malformed run %d", count)
		}
	}
}

func BenchmarkJSONMayEscapePlainASCII(b *testing.B) {
	code := strings.Repeat("func f() { return \\\"sample\\\" }\\n", 1<<15)
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"short", "{\"input\":\"ok\"}", false},
		{"short-newline", "{\"input\":\"a\\nb\"}", false},
		{"plain", strings.Repeat("function sample ", 1<<16), false},
		{"quoted-code", code, false},
		{"source-file", strings.Repeat("package app\\nfunc sample() string { return \\\"Example 测试\\\" }\\n", 1<<14), false},
		{"sparse-newlines", strings.Repeat(strings.Repeat("x", 126)+"\\n", 1<<13), false},
		{"late-keyword-escape", code + "\\u0061", true},
		{"html-escapes", strings.Repeat("\\u003c\\u003e", 1<<16), false},
		{"escaped-backslashes", strings.Repeat("\\\\", 1<<19), false},
	} {
		b.Run(tc.name, func(b *testing.B) {
			body := []byte(tc.body)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				if JSONMayEscapePlainASCII(body) != tc.want {
					b.Fatal("unexpected escape classification")
				}
			}
		})
	}
}
