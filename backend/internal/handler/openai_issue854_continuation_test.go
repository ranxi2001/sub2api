//go:build unit

package handler

import "testing"

// Real authentication, scheduler and forwarders; only repositories and paid
// providers are fixtures. B must validate tools, completed results and absence
// of A's ciphertext/pointers before completing the remaining tool loop.
func TestIssue854OwnerContinuation(t *testing.T) {
	for _, mode := range readableWSModes {
		for _, connection := range []string{"original", "fresh", "guard_live"} {
			t.Run(mode+"/"+connection, func(t *testing.T) {
				runReadableWSContinuation(t, mode, "issue854_"+connection)
			})
		}
	}
}
