package handler

import (
	"bytes"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// mayContainCodexCallOutputCandidate is a necessary condition for a bootstrap
// candidate, checked before the full uniqueness scan and decode. Its type and
// tool name are JSON strings, which appear literally unless a \u escape spells
// their letters; without either, normalization cannot change the body. Escapes
// that real clients emit (control characters, HTML-safe and non-ASCII runes)
// cannot spell those letters and must not force the full decode.
//
// Tool declarations carry the same names, so a literal hit is confirmed by a
// zero-copy scan for an input item of type function_call_output whose name
// starts with one of toolNames. The full path rejects bodies with duplicate
// members, and without duplicates the scan reads the same values as the
// decoder.
func mayContainCodexCallOutputCandidate(body []byte, toolNames ...string) bool {
	if !service.JSONMayEscapePlainASCII(body) && !containsCodexCallOutputLiterals(body, toolNames) {
		return false
	}
	return service.ResponsesInputHasFunctionCallOutputNamed(body, toolNames...)
}

func containsCodexCallOutputLiterals(body []byte, toolNames []string) bool {
	if !bytes.Contains(body, []byte("function_call_output")) {
		return false
	}
	for _, name := range toolNames {
		if bytes.Contains(body, []byte(name)) {
			return true
		}
	}
	return false
}
