package service

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func BenchmarkMissingSessionIdentityLargeBody(b *testing.B) {
	body := append([]byte(`{"input":"`), bytes.Repeat([]byte("x"), 8<<20)...)
	body = append(body, []byte(`"}`)...)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := resolveOpenAIClientSessionIdentity(c, body); got.metadata.Status != OpenAIClientSessionIdentityMissing {
			b.Fatal("unexpected identity")
		}
	}
}

func TestMissingIdentityAvoidsUnnecessaryJSONAllocations(t *testing.T) {
	fastBody := append([]byte(`{"input":"`), bytes.Repeat([]byte("x"), 1<<20)...)
	fastBody = append(fastBody, []byte(`"}`)...)
	escapedBody := append([]byte(`{"input":"\u0020`), bytes.Repeat([]byte("x"), 1<<20)...)
	escapedBody = append(escapedBody, []byte(`"}`)...)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	allocations := func(body []byte) float64 {
		return testing.AllocsPerRun(20, func() {
			if got := resolveOpenAIClientSessionIdentity(c, body); got.metadata.Status != OpenAIClientSessionIdentityMissing {
				t.Fatal("unexpected identity")
			}
		})
	}
	fast, escaped := allocations(fastBody), allocations(escapedBody)
	if fast >= escaped {
		t.Fatalf("plain request incurred %.0f allocations; escaped fallback incurred %.0f", fast, escaped)
	}
}

func TestSessionIdentityHandlesEscapedMetadataAndConflicts(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	body := []byte(`{"client_\u006detadata":{"thread_id":"body-thread"}}`)
	if got := resolveOpenAIClientSessionIdentity(c, body); got.metadata.Status != OpenAIClientSessionIdentityResolved || got.identity.value != "body-thread" {
		t.Fatalf("escaped metadata not resolved: %#v", got.metadata)
	}
	c.Request.Header.Set("thread-id", "other-thread")
	if got := resolveOpenAIClientSessionIdentity(c, body); got.metadata.Status != OpenAIClientSessionIdentityConflict {
		t.Fatalf("conflicting identity accepted: %#v", got.metadata)
	}
}

func FuzzMissingSessionMetadataPrefilter(f *testing.F) {
	for _, body := range [][]byte{
		[]byte(`{"input":"ordinary"}`),
		[]byte(`{"type":"response.create","response":{"input":[]}}`),
		[]byte(`{"client_\u006detadata":{"thread_id":"id"}}`),
		[]byte(`{"client_metadata":{"thread_id":"id"}}`),
		[]byte(`{"input":"broken`),
	} {
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 || bytes.Contains(body, []byte("client_metadata")) || bytes.Contains(body, []byte(`\u`)) {
			return
		}
		root := parseRawJSONView(body)
		for _, field := range []string{"thread_id", "session_id"} {
			value := openAIClientMetadataIdentityAcrossEnvelope(root, field)
			if value.status != OpenAIClientSessionIdentityMissing || value.value != "" {
				t.Fatal("metadata prefilter missed an identity or rejection")
			}
		}
	})
}
