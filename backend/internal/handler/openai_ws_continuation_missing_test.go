//go:build unit && unsentdelta && continuationmissing

package handler

import "testing"

func TestOpenAIWSMissingHistorySendsCanonicalRetry(t *testing.T) {
	for _, mode := range readableWSModes {
		t.Run(mode, func(t *testing.T) {
			runUnsentDeltaWithInput(t, mode, 4, "missing_history", `,{"type":"fixture_unsupported_history","text":"fixture extra history"}`)
		})
	}
}
