//go:build unit

package handler

import (
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"testing"
)

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

func TestIssue854OwnerFailurePersistence(t *testing.T) {
	for _, mode := range readableWSModes {
		t.Run(mode, func(t *testing.T) {
			runReadableWSContinuation(t, mode, "issue854_guard_refusal")
		})
	}
}

func TestIssue854HTTPContinuation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			runHTTPRebuildFixture(t, service.AccountTypeAPIKey, stream, "previous", "completed", "issue854_auth", "")
		})
	}
}
