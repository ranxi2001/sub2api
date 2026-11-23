package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSTerminalReplayPreservesAuthoritativeOutput(t *testing.T) {
	var collector openAIWSToolCallReplayCollector
	const streamed = `{"type":"message","id":"msg","content":"draft"}`
	const final = `{"type":"message","id":"msg","content":"final"}`
	const call = `{"type":"function_call","id":"fc","call_id":"call","name":"example","arguments":"{}"}`
	collector.AddEvent("response.output_item.done", []byte(`{"item":`+streamed+`}`))
	collector.AddEvent("response.completed", []byte(`{"response":{"status":"completed","output":[`+final+`,`+call+`]}}`))

	// An empty completion cannot replace the last authoritative output. An
	// incomplete turn can contribute replay items but cannot replace it either.
	collector.AddEvent("response.done", []byte(`{"response":{"status":"completed","output":[]}}`))
	const partial = `{"type":"message","id":"partial"}`
	collector.AddEvent("response.done", []byte(`{"response":{"status":"incomplete","output":[`+partial+`]}}`))

	require.Len(t, collector.finalItems, 2)
	require.Equal(t, final, string(collector.finalItems[0]))
	require.Equal(t, call, string(collector.finalItems[1]))
	all := collector.AllItems()
	require.Len(t, all, 3)
	require.Equal(t, streamed, string(all[0]))
	require.Equal(t, call, string(all[1]))
	require.Equal(t, partial, string(all[2]))
	items := collector.Items()
	require.Len(t, items, 1)
	require.Equal(t, call, string(items[0]))
}

func BenchmarkOpenAIWSTerminalReplayOutput(b *testing.B) {
	for _, tc := range []struct {
		status string
		count  int
	}{
		{"completed", 0},
		{"completed", 1},
		{"completed", 32},
		{"completed", 256},
		{"incomplete", 32},
	} {
		b.Run(fmt.Sprintf("%s/%d", tc.status, tc.count), func(b *testing.B) {
			fields := make([]string, tc.count)
			for i := range fields {
				fields[i] = fmt.Sprintf(`{"type":"message","id":"msg_%d","role":"assistant","content":[{"type":"output_text","text":"fixture"}]}`, i)
			}
			event := []byte(`{"type":"response.completed","response":{"status":"` + tc.status + `","output":[` + strings.Join(fields, ",") + `]}}`)
			b.ReportAllocs()
			b.SetBytes(int64(len(event)))
			for b.Loop() {
				var collector openAIWSToolCallReplayCollector
				collector.AddEvent("response.completed", event)
				if len(collector.allItems) != tc.count {
					b.Fatalf("got %d replay items, want %d", len(collector.allItems), tc.count)
				}
			}
		})
	}
}
