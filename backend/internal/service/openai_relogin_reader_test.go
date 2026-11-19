package service

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReadGuardResultDistinguishesIdleMalformedAndOversize(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		body := &blockingGuardBody{}
		_, _, err := readGuardResult(context.Background(), body, 100, 20*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "idle timeout") {
			t.Fatalf("err = %v, want idle timeout", err)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		_, _, err := readGuardResult(context.Background(), io.NopCloser(strings.NewReader(`{"type":"result"`)), 100, time.Second)
		if err == nil || !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("err = %v, want malformed", err)
		}
	})
	t.Run("oversize", func(t *testing.T) {
		_, _, err := readGuardResult(context.Background(), io.NopCloser(strings.NewReader(`{"message":"`+strings.Repeat("x", 200)+`"}`)), 32, time.Second)
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("err = %v, want too large", err)
		}
	})
}

type blockingGuardBody struct {
	mu   sync.Mutex
	once sync.Once
	done chan struct{}
}

func (b *blockingGuardBody) Read([]byte) (int, error) {
	b.mu.Lock()
	if b.done == nil {
		b.done = make(chan struct{})
	}
	done := b.done
	b.mu.Unlock()
	<-done
	return 0, io.EOF
}

func (b *blockingGuardBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done == nil {
		b.done = make(chan struct{})
	}
	b.once.Do(func() { close(b.done) })
	return nil
}
