//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Keep the test's adapter contract independent of newly added production types:
// the very same test also compiles against the pinned pre-fix implementation.
type issue715DeliveryOwners interface {
	GetOpenAIAgentRecovery(context.Context, string) (string, error)
	PublishOpenAIAgentRecovery(context.Context, string, string, time.Duration) error
}

func issue715RequireDeliveryOwners(t *testing.T, base service.GatewayCache) issue715DeliveryOwners {
	t.Helper()
	owners, ok := base.(issue715DeliveryOwners)
	require.True(t, ok)
	return owners
}

type issue715SnapshotCache struct {
	service.GatewayCache
	issue715DeliveryOwners
	captured, release chan struct{}
	once              sync.Once
}

func (c *issue715SnapshotCache) ReserveOpenAIAgentRecovery(ctx context.Context, key, route, pending string, ttl time.Duration) error {
	adapter, ok := c.issue715DeliveryOwners.(interface {
		ReserveOpenAIAgentRecovery(context.Context, string, string, string, time.Duration) error
	})
	if !ok {
		return fmt.Errorf("fixture old adapter has no pending support")
	}
	return adapter.ReserveOpenAIAgentRecovery(ctx, key, route, pending, ttl)
}
func (c *issue715SnapshotCache) FinishOpenAIAgentRecovery(ctx context.Context, key, route, pending string, commit bool, ttl time.Duration) error {
	adapter, ok := c.issue715DeliveryOwners.(interface {
		FinishOpenAIAgentRecovery(context.Context, string, string, string, bool, time.Duration) error
	})
	if !ok {
		return fmt.Errorf("fixture old adapter has no pending support")
	}
	return adapter.FinishOpenAIAgentRecovery(ctx, key, route, pending, commit, ttl)
}

func (c *issue715SnapshotCache) GetOpenAIAgentRecovery(ctx context.Context, key string) (string, error) {
	raw, err := c.issue715DeliveryOwners.GetOpenAIAgentRecovery(ctx, key)
	c.once.Do(func() {
		close(c.captured)
		select {
		case <-c.release:
		case <-ctx.Done():
		}
	})
	return raw, err
}

type issue715BarrierWriter struct {
	gin.ResponseWriter
	body               strings.Builder
	delivered, release chan struct{}
	once               sync.Once
	stream             bool
	failure            string
	cancel             context.CancelFunc
}

func (w *issue715BarrierWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseWriter.Write(body)
	_, _ = w.body.Write(body[:n])
	payload := w.body.String()
	ready := strings.Contains(payload, "fixture-task-reference-715") && strings.Contains(payload, "update_ledger")
	if w.stream {
		ready = ready && strings.Contains(payload, "response.completed") && strings.HasSuffix(payload, "\n\n")
	}
	if ready {
		w.once.Do(func() { w.ResponseWriter.Flush(); close(w.delivered); <-w.release })
	}
	if ready {
		switch w.failure {
		case "error":
			return 0, io.ErrClosedPipe
		case "short":
			return len(body) - 1, nil
		case "cancel":
			w.cancel()
		}
	}
	return n, err
}
func (w *issue715BarrierWriter) WriteString(body string) (int, error) { return w.Write([]byte(body)) }

func issue715Await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("fixture barrier not reached")
	}
}

func TestIssue715HTTPFirstDeliveryCrossNodeBarrier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct{ format, failure string }{
		{format: "generated-json"}, {format: "generated-stream"}, {format: "generated-stream-items"}, {format: "generated-stream-multiline"}, {format: "generated-stream-keepalive"},
		{format: "generated-json", failure: "error"}, {format: "generated-json", failure: "short"}, {format: "generated-json", failure: "cancel"}, {format: "generated-stream-items", failure: "cancel"},
	} {
		format := test.format
		name := format
		if test.failure != "" {
			name += "-" + test.failure
		}
		t.Run(name, func(t *testing.T) {
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
			defer client.Close()
			base := repository.NewGatewayCache(client)
			upstream := &issue715Upstream{mode: format}
			producer := newOpenAIResponsesFailoverTestHandlerWithCache(t, upstream, base, issue715Accounts()...)
			stream := strings.HasPrefix(format, "generated-stream-")
			first := strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1)
			c, _ := newIssue715Context(t, first)
			c.Request.Header.Set("session_id", "fixture-715-thread")
			requestCtx, cancel := context.WithCancel(c.Request.Context())
			defer cancel()
			c.Request = c.Request.WithContext(requestCtx)
			writer := &issue715BarrierWriter{ResponseWriter: c.Writer, delivered: make(chan struct{}), release: make(chan struct{}), stream: stream, failure: test.failure, cancel: cancel}
			c.Writer = writer
			var releaseOnce sync.Once
			releaseProducer := func() { releaseOnce.Do(func() { close(writer.release) }) }
			defer releaseProducer()
			producerDone := make(chan struct{})
			go func() { defer close(producerDone); producer.Responses(c) }()
			issue715Await(t, writer.delivered)
			select {
			case <-producerDone:
				t.Fatal("producer returned before client-visible barrier")
			default:
			}
			// The client has the complete task and function call; actually apply it once.
			payload := writer.body.String()
			if stream {
				for _, line := range strings.Split(payload, "\n") {
					if strings.HasPrefix(line, "data: ") && gjson.Get(strings.TrimPrefix(line, "data: "), "type").String() == "response.completed" {
						payload = gjson.Get(strings.TrimPrefix(line, "data: "), "response").Raw
					}
				}
			}
			toolExecutions, balance := 0, 11
			for _, item := range gjson.Get(payload, "output").Array() {
				if item.Get("type").String() == "function_call" {
					toolExecutions++
					balance += int(gjson.Get(item.Get("arguments").String(), "add").Int())
				}
			}
			require.Equal(t, 1, toolExecutions)
			require.Equal(t, 18, balance)
			cache := &issue715SnapshotCache{GatewayCache: base, issue715DeliveryOwners: issue715RequireDeliveryOwners(t, base), captured: make(chan struct{}), release: make(chan struct{})}
			var readOnce sync.Once
			releaseReader := func() { readOnce.Do(func() { close(cache.release) }) }
			defer releaseReader()
			accounts := issue715Accounts()
			accounts[0].Priority, accounts[1].Priority = 100, 0
			consumer := newOpenAIResponsesFailoverTestHandlerWithCache(t, upstream, cache, accounts...)
			probe, _ := newIssue715Context(t, issue715Body(true, stream))
			probe.Request.Header.Set("session_id", "fixture-715-thread")
			hash := consumer.gatewayService.GenerateSessionHash(probe, []byte(issue715Body(true, stream)))
			require.NoError(t, base.SetSessionAccountID(context.Background(), 3132, "openai:"+hash, 2, time.Minute))
			type answer struct {
				status int
				body   string
			}
			result := make(chan answer, 1)
			go func() {
				status, body := issue715Request(t, consumer, issue715Body(true, stream), 99, "fixture-715-thread", false)
				result <- answer{status, body}
			}()
			// Capture the authoritative lookup while producer Write has not returned.
			// Old code deterministically captures miss; fixed code captures pending.
			issue715Await(t, cache.captured)
			releaseProducer()
			issue715Await(t, producerDone)
			require.NoError(t, base.SetSessionAccountID(context.Background(), 3132, "openai:"+hash, 2, time.Minute))
			releaseReader()
			var got answer
			select {
			case got = <-result:
			case <-time.After(10 * time.Second):
				t.Fatal("consumer did not finish")
			}
			if test.failure != "" {
				require.Equal(t, 409, got.status, got.body)
				require.Contains(t, got.body, "encrypted_agent_recovery_owner_unavailable")
				upstream.mu.Lock()
				ids := append([]int64(nil), upstream.ids...)
				upstream.mu.Unlock()
				require.Equal(t, []int64{1}, ids, "failed first delivery must neither grant nor route to an unrelated account")
				return
			}
			require.Contains(t, got.body, "Same task: ledger balance 18", "client-visible first delivery must not race shared ownership")
			require.Equal(t, 200, got.status, got.body)
			require.NotContains(t, got.body, `"type":"function_call"`)
			for turn := 0; turn < 2; turn++ {
				status, body := issue715Request(t, consumer, issue715Body(true, stream), 99, "fixture-715-thread", false)
				require.Equal(t, 200, status, body)
				require.Contains(t, body, "Same task: ledger balance 18")
				require.NotContains(t, body, `"type":"function_call"`)
			}
			upstream.mu.Lock()
			ids := append([]int64(nil), upstream.ids...)
			bodies := append([][]byte(nil), upstream.bodies...)
			upstream.mu.Unlock()
			require.Equal(t, []int64{1, 1, 1, 1}, ids, fmt.Sprint(ids))
			for _, body := range bodies[1:] {
				require.Contains(t, string(body), "fixture-task-reference-715")
				require.Contains(t, string(body), "ledger balance 18; applied once")
			}
			require.Equal(t, 1, toolExecutions)
		})
	}
}

var _ io.StringWriter = (*issue715BarrierWriter)(nil)

type issue715FailingDeliveryCache struct {
	service.GatewayCache
	issue715DeliveryOwners
	mode     string
	reserves int
}

func (c *issue715FailingDeliveryCache) ReserveOpenAIAgentRecovery(ctx context.Context, key, route, pending string, ttl time.Duration) error {
	if c.mode == "reserve" {
		return io.ErrClosedPipe
	}
	adapter, ok := c.issue715DeliveryOwners.(interface {
		ReserveOpenAIAgentRecovery(context.Context, string, string, string, time.Duration) error
	})
	if !ok {
		return fmt.Errorf("fixture old adapter has no pending support")
	}
	c.reserves++
	if c.mode == "partial_reserve" && c.reserves == 2 {
		return io.ErrClosedPipe
	}
	err := adapter.ReserveOpenAIAgentRecovery(ctx, key, route, pending, ttl)
	if err == nil && c.mode == "reserve_applied" {
		return io.ErrClosedPipe
	}
	return err
}
func (c *issue715FailingDeliveryCache) FinishOpenAIAgentRecovery(ctx context.Context, key, route, pending string, commit bool, ttl time.Duration) error {
	if c.mode == "commit" && commit {
		return io.ErrClosedPipe
	}
	adapter, ok := c.issue715DeliveryOwners.(interface {
		FinishOpenAIAgentRecovery(context.Context, string, string, string, bool, time.Duration) error
	})
	if !ok {
		return fmt.Errorf("fixture old adapter has no pending support")
	}
	err := adapter.FinishOpenAIAgentRecovery(ctx, key, route, pending, commit, ttl)
	if err == nil && commit && c.mode == "commit_applied" {
		return io.ErrClosedPipe
	}
	return err
}

func TestIssue715HTTPDeliveryPublicationFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, failure := range []string{"reserve", "commit", "reserve_applied", "commit_applied", "partial_reserve"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprint(failure, "/", stream), func(t *testing.T) {
				mr := miniredis.RunT(t)
				client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
				defer client.Close()
				base := repository.NewGatewayCache(client)
				cache := &issue715FailingDeliveryCache{GatewayCache: base, issue715DeliveryOwners: issue715RequireDeliveryOwners(t, base), mode: failure}
				mode := "generated-json"
				if failure == "partial_reserve" {
					mode = "generated-json-two"
				}
				upstream := &issue715Upstream{mode: mode}
				producer := newOpenAIResponsesFailoverTestHandlerWithCache(t, upstream, cache, issue715Accounts()...)
				first := strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1)
				status, body := issue715Request(t, producer, first, 99, "fixture-715-thread", false)
				require.Equal(t, []int64{1}, upstream.ids, "publication failure cannot replay a submitted generation")
				if failure == "reserve" || failure == "reserve_applied" || failure == "partial_reserve" {
					require.NotContains(t, body, "fixture-task-reference-715", "reserve failure must withhold the unprotected generated task")
					require.Contains(t, body, "encrypted_agent_recovery_store_unavailable")
					for _, key := range mr.Keys() {
						if strings.HasPrefix(key, "openai_agent_owner:") {
							value, err := mr.Get(key)
							require.NoError(t, err)
							require.Equal(t, "failed", value, "partial/ambiguous reserve must clean up every applied fence")
						}
					}
					return
				}
				require.Equal(t, 200, status, body)
				require.Contains(t, body, "fixture-task-reference-715")
				accounts := issue715Accounts()
				accounts[0].Priority, accounts[1].Priority = 100, 0
				consumer := newOpenAIResponsesFailoverTestHandlerWithCache(t, upstream, base, accounts...)
				status, body = issue715Request(t, consumer, issue715Body(true, false), 99, "fixture-715-thread", false)
				if failure == "commit_applied" {
					require.Equal(t, 200, status, body)
					require.Contains(t, body, "Same task: ledger balance 18")
					require.Equal(t, []int64{1, 1}, upstream.ids, "ambiguous acknowledged commit must preserve its successful CAS")
					return
				}
				require.Equal(t, 409, status, body)
				require.Contains(t, body, "encrypted_agent_recovery_delivery_pending")
				require.Equal(t, []int64{1}, upstream.ids, "failed commit must retain pending and never fall through to ordinary routing")
			})
		}
	}
}
