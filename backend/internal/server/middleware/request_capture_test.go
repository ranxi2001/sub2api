package middleware

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/requestcapture"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type captureMiddlewareStore struct {
	requestcapture.Store
	mu      sync.Mutex
	tasks   map[string]requestcapture.Task
	records map[string]requestcapture.Record
}

func (s *captureMiddlewareStore) Tasks(context.Context, string, int, int) ([]requestcapture.Task, error) {
	return nil, nil
}
func (s *captureMiddlewareStore) SaveTask(_ context.Context, t *requestcapture.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[t.ID] = *t
	return nil
}
func (s *captureMiddlewareStore) SaveRecord(_ context.Context, r *requestcapture.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(r)
	var copy requestcapture.Record
	_ = json.Unmarshal(b, &copy)
	s.records[r.ID] = copy
	return nil
}
func TestRequestCaptureMiddlewarePreservesCompressedInputAndRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &captureMiddlewareStore{tasks: map[string]requestcapture.Task{}, records: map[string]requestcapture.Record{}}
	dir := t.TempDir()
	m, err := requestcapture.New(store, dir, requestcapture.Config{Enabled: true, QuotaMiB: 10, RetentionDays: 7})
	require.NoError(t, err)
	defer m.Close()
	target, err := m.Create(context.Background(), requestcapture.CreateTask{TargetType: "group", TargetID: 7, DurationMinutes: 1}, "fixture")
	require.NoError(t, err)
	r := gin.New()
	r.Use(ClientRequestID())
	r.Use(func(c *gin.Context) {
		group := int64(7)
		c.Set(string(ContextKeyAPIKey), &service.APIKey{UserID: 1, GroupID: &group})
		c.Next()
	})
	r.Use(RequestCapture(m))
	input := []byte(`{"model":"original","api_key":"SECRET"}`)
	r.POST("/responses", func(c *gin.Context) {
		body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
		require.NoError(t, err)
		require.Equal(t, input, body)
		routed := int64(9)
		c.Set(string(ContextKeyAPIKey), &service.APIKey{UserID: 1, GroupID: &routed})
		c.Header("Content-Type", "text/event-stream")
		_, _ = c.Writer.WriteString("data: {\"type\":\"error\",\"error\":{\"code\":\"local_reject\"}}\n\n")
		c.Writer.Flush()
	})
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, err = gz.Write(input)
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	req := httptest.NewRequest(http.MethodPost, "/responses", &compressed)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer AUTH-SECRET")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "local_reject")
	require.Eventually(t, func() bool { return m.Stats().ActiveRequests == 0 }, 5*time.Second, 10*time.Millisecond)
	store.mu.Lock()
	defer store.mu.Unlock()
	require.Len(t, store.records, 1)
	for _, record := range store.records {
		require.Equal(t, target.ID, record.TaskID)
		require.EqualValues(t, 7, record.GroupID)
		require.EqualValues(t, 9, record.RoutedGroupID)
		require.Equal(t, w.Header().Get("X-Client-Request-ID"), record.RequestID)
		require.True(t, record.IsError)
		require.False(t, record.Partial, record.Reason)
		require.Len(t, record.Parts, 2)
		for _, part := range record.Parts {
			file, err := os.Open(filepath.Join(dir, target.ID, record.ID, part.Name))
			require.NoError(t, err)
			b, err := io.ReadAll(file)
			require.NoError(t, err)
			require.NoError(t, file.Close())
			require.NotContains(t, string(b), "SECRET")
		}
	}
}
func TestCaptureSensitiveReadAuditRoutes(t *testing.T) {
	for _, route := range []string{"GET /api/v1/admin/request-captures/:task/requests/:record", "GET /api/v1/admin/request-captures/:task/requests/:record/content/:part", "GET /api/v1/admin/request-captures/:task/export", "GET /api/v1/admin/request-captures/:task/requests/:record/export"} {
		require.NotEmpty(t, auditSensitiveReads[route])
	}
}

type captureFlushFailureRecorder struct {
	*httptest.ResponseRecorder
	failAt  int
	flushes int
}

func (w *captureFlushFailureRecorder) FlushError() error {
	w.flushes++
	if w.flushes == w.failAt {
		return io.ErrClosedPipe
	}
	w.Flush()
	return nil
}

func TestRequestCapturePropagatesFlushFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failAt   int
		wantBody string
	}{
		{"intermediate", 1, "data: content\n\n"},
		{"final", 2, "data: content\n\ndata: [DONE]\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &captureMiddlewareStore{tasks: map[string]requestcapture.Task{}, records: map[string]requestcapture.Record{}}
			manager, err := requestcapture.New(store, t.TempDir(), requestcapture.Config{Enabled: true, QuotaMiB: 10, RetentionDays: 7})
			require.NoError(t, err)
			defer manager.Close()
			_, err = manager.Create(context.Background(), requestcapture.CreateTask{TargetType: "group", TargetID: 7, DurationMinutes: 1}, "fixture")
			require.NoError(t, err)
			r := gin.New()
			r.Use(ClientRequestID(), func(c *gin.Context) {
				group := int64(7)
				c.Set(string(ContextKeyAPIKey), &service.APIKey{UserID: 1, GroupID: &group})
				c.Next()
			}, RequestCapture(manager))
			var flushErr error
			r.GET("/stream", func(c *gin.Context) {
				require.IsType(t, &captureResponseWriter{}, c.Writer)
				c.Header("Content-Type", "text/event-stream")
				for _, frame := range []string{"data: content\n\n", "data: [DONE]\n\n"} {
					_, err := c.Writer.WriteString(frame)
					require.NoError(t, err)
					flushErr = service.FlushGatewayResponse(c.Writer)
					if flushErr != nil {
						return
					}
				}
			})
			writer := &captureFlushFailureRecorder{ResponseRecorder: httptest.NewRecorder(), failAt: tc.failAt}
			r.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/stream", nil))
			require.ErrorIs(t, flushErr, io.ErrClosedPipe)
			require.Equal(t, tc.failAt, writer.flushes)
			require.Equal(t, tc.wantBody, writer.Body.String())
			require.Equal(t, http.StatusOK, writer.Code)
			require.Eventually(t, func() bool {
				store.mu.Lock()
				defer store.mu.Unlock()
				for _, record := range store.records {
					if record.Partial && record.IsError {
						return true
					}
				}
				return false
			}, 5*time.Second, 10*time.Millisecond)
		})
	}
}
