package service

import (
	"bytes"
	"testing"
)

func FuzzNoCompactionFastPathMatchesDecoder(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"input":[{"type":"message"}]}`),
		[]byte(`{"input":[],"input":[{"type":"message"}],"n":1e99999}`),
		[]byte(`{"x":1} true`), []byte(`{"x":01}`), []byte(`[{}]`),
		[]byte(`{"x":"\ud800"}`), append([]byte(`{"x":"`), 0xff, '"', '}'),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) == 0 || bytes.Contains(body, []byte("compaction_trigger")) || bytes.Contains(body, []byte(`\u`)) {
			return
		}
		var payload map[string]any
		wantErr := decodeOpenAIJSONUseNumber(body, &payload)
		out, changed, err := NormalizeCompactionTriggerInputOrder(body)
		if changed || (err != nil) != (wantErr != nil) || !bytes.Equal(out, body) {
			t.Fatalf("no-trigger behavior differs: changed=%v err=%v baseline=%v", changed, err, wantErr)
		}
	})
}
