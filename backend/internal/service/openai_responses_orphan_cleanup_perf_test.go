package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// This fixture contains no real messages or network access.
func syntheticOAuthMessageHistory() []byte {
	var body bytes.Buffer
	_, _ = body.WriteString(`{"model":"gpt-5.4","input":[`)
	content := strings.Repeat("synthetic ", 128)
	for i := 0; i < 2000; i++ {
		if i != 0 {
			_ = body.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&body, `{"type":"message","id":"msg_%06d","role":"user","content":[{"type":"input_text","text":%q}]}`, i, content)
	}
	_, _ = body.WriteString(`]}`)
	return body.Bytes()
}

func TestOAuthCompatibilityMessageHistoryAvoidsRedundantDecode(t *testing.T) {
	// Allocation accounting is process-wide; isolate it from other service workers.
	const childEnv = "SUB2API_OPENAI_ORPHAN_ALLOC_TEST_CHILD"
	if os.Getenv(childEnv) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, executable, "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated allocation test failed: %v\n%s", err, output)
		}
		return
	}
	request := syntheticOAuthMessageHistory()
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	allocations := testing.AllocsPerRun(2, func() {
		if _, _, err := normalizeOpenAIResponsesWebSocketCompatibilityBody(request, account, false); err != nil {
			t.Fatal(err)
		}
	})
	// The previous full map decode adds tens of thousands of allocations for
	// this history even though no tool output can be removed.
	if allocations > 90000 {
		t.Fatalf("message-only history allocated %.0f times; maximum 90000", allocations)
	}
}

// Escaped message strings must be scanned without decoding or copying them.
func TestOrphanCleanupFastRejectionLargeEscapedContent(t *testing.T) {
	const childEnv = "SUB2API_OPENAI_ORPHAN_ESCAPED_ALLOC_TEST_CHILD"
	if os.Getenv(childEnv) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, executable, "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		started := time.Now()
		output, err := cmd.CombinedOutput()
		t.Logf("isolated escaped allocation child elapsed=%s", time.Since(started))
		if err != nil {
			t.Fatalf("isolated escaped allocation test failed: %v\n%s", err, output)
		}
		t.Logf("%s", output)
		return
	}
	escaped := strings.Repeat(`\n\"`, 1<<16)
	body := []byte(`{"vendor":"` + escaped + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + escaped + `"}]}]}`)
	unexpectedCleanup := false
	measurement := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if openAIResponsesMayNeedOrphanCleanup(body) {
				unexpectedCleanup = true
				b.Fatal("escaped message-only input unexpectedly requires orphan cleanup")
			}
		}
	})
	t.Logf("escaped allocation measurement N=%d B/op=%d elapsed=%s", measurement.N, measurement.AllocedBytesPerOp(), measurement.T)
	// A standalone Benchmark has no parent T: b.Fatal alone cannot fail this test.
	if unexpectedCleanup {
		t.Fatal("escaped message-only input unexpectedly requires orphan cleanup")
	}
	if measurement.N <= 0 || measurement.T <= 0 {
		t.Fatalf("invalid escaped allocation measurement: N=%d elapsed=%s", measurement.N, measurement.T)
	}
	// json.Valid borrows a small scanner from sync.Pool; race instrumentation
	// randomly discards pooled objects. Bound bytes instead of requiring pool
	// hits: copying or decoding this large escaped body still fails the guard.
	if allocated := measurement.AllocedBytesPerOp(); allocated > 1024 {
		t.Fatalf("escaped message-only scan allocated %d bytes per call; maximum 1024", allocated)
	}
}

// Exercise a large message-only Responses history through OAuth compatibility.
func BenchmarkOAuthResponsesCompatibilityMessageHistory(b *testing.B) {
	request := syntheticOAuthMessageHistory()
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	b.SetBytes(int64(len(request)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := normalizeOpenAIResponsesWebSocketCompatibilityBody(request, account, false); err != nil {
			b.Fatal(err)
		}
	}
}
