package service

import (
	"bytes"
	"testing"
)

func TestNoCompactionTriggerAvoidsFullDecode(t *testing.T) {
	body := append([]byte(`{"input":[{"type":"function_call_output","output":"`), bytes.Repeat([]byte("x"), 1<<20)...)
	body = append(body, []byte(`"}]}`)...)
	allocs := testing.AllocsPerRun(3, func() {
		out, changed, err := NormalizeCompactionTriggerInputOrder(body)
		if err != nil || changed || !bytes.Equal(out, body) {
			t.Fatalf("no-trigger request changed or failed: changed=%v err=%v", changed, err)
		}
	})
	if allocs > 2 {
		t.Fatalf("no-trigger request allocated %.0f times, want at most 2", allocs)
	}
}

func TestNoCompactionTriggerPreservesDecoderValidation(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{"input":[],"input":[{"type":"message"}]}`),
		[]byte(`{"input":[{"type":false}],"n":1e99999}`),
		[]byte(`{"input":"text"}`), []byte(`{"x":null} `),
		[]byte(`{"x":1} true`), []byte(`{"x":01}`), []byte(`[{}]`),
		[]byte(`null`), []byte(`true`), []byte(`{"x":"\ud800"}`),
		append(append([]byte(`{"input":"`), 0xff), []byte(`"}`)...),
	}
	for _, body := range bodies {
		var payload map[string]any
		wantErr := decodeOpenAIJSONUseNumber(body, &payload)
		out, changed, err := NormalizeCompactionTriggerInputOrder(body)
		if changed || (err != nil) != (wantErr != nil) || !bytes.Equal(out, body) {
			t.Fatalf("validation differs for %q: changed=%t err=%v baseline=%v", body, changed, err, wantErr)
		}
	}
}

func TestEscapedCompactionTriggerStillMovesToEnd(t *testing.T) {
	body := []byte(`{"input":[{"type":"compaction_\u0074rigger"},{"type":"message"}]}`)
	out, changed, err := NormalizeCompactionTriggerInputOrder(body)
	if err != nil || !changed || !bytes.Contains(out, []byte(`"type":"compaction_trigger"`)) {
		t.Fatalf("escaped trigger not normalized: changed=%v err=%v", changed, err)
	}
}
