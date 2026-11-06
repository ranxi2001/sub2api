package service

import (
	"fmt"
	"testing"
)

// legacyStringScan preserves the pre-optimization validation contract for differential fuzzing.
func legacyStringScan(p *openAIResponsesToolSchemaParser) (int, int, error) {
	if p.pos >= len(p.body) || p.body[p.pos] != '"' {
		return 0, 0, p.syntaxError("expected string")
	}
	start := p.pos
	p.pos++
	for p.pos < len(p.body) {
		switch p.body[p.pos] {
		case '"':
			p.pos++
			return start, p.pos, nil
		case '\\':
			p.pos++
			if p.pos >= len(p.body) {
				return 0, 0, p.syntaxError("unterminated string escape")
			}
			switch p.body[p.pos] {
			case 'u':
				if p.pos+4 >= len(p.body) {
					return 0, 0, p.syntaxError("short unicode escape")
				}
				for _, digit := range p.body[p.pos+1 : p.pos+5] {
					if !isOpenAIResponsesJSONHexDigit(digit) {
						return 0, 0, p.syntaxError("invalid unicode escape")
					}
				}
				p.pos += 5
				continue
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				p.pos++
			default:
				return 0, 0, p.syntaxError("invalid string escape")
			}
		default:
			if p.body[p.pos] < 0x20 {
				return 0, 0, p.syntaxError("control character in string")
			}
			p.pos++
		}
	}
	return 0, 0, p.syntaxError("unterminated string")
}

func FuzzStringScanLegacyEquivalence(f *testing.F) {
	for _, seed := range [][]byte{[]byte(`"hello"`), []byte(`"a\u0022b"`), {'"', 0xff, '"'}, {'"', 0, '"'}, []byte(`"a\q"`), []byte(`"unfinished`)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 65536 {
			t.Skip()
		}
		for _, body := range [][]byte{data, append([]byte{'"'}, data...)} {
			a := openAIResponsesToolSchemaParser{body: body}
			b := openAIResponsesToolSchemaParser{body: body}
			as, ae, ax := a.parseString()
			bs, be, bx := legacyStringScan(&b)
			if as != bs || ae != be || a.pos != b.pos || fmt.Sprint(ax) != fmt.Sprint(bx) {
				t.Fatalf("scanner differs: %q", body)
			}
		}
	})
}
