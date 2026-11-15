//go:build unit && plaintextowner

package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/requestcapture"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Persist snapshots rather than live pointers while the real capture worker
// finalizes frames. Payloads use the manager's real bounded filesystem writer.
type recoveryCaptureStore struct {
	mu      sync.Mutex
	tasks   map[string][]byte
	records map[string][]byte
}

func (s *recoveryCaptureStore) SaveTask(_ context.Context, task *requestcapture.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(task)
	s.tasks[task.ID] = raw
	return err
}
func (s *recoveryCaptureStore) Task(_ context.Context, instance, id string) (*requestcapture.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.tasks[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	var task requestcapture.Task
	err := json.Unmarshal(raw, &task)
	if task.InstanceID != instance {
		return nil, sql.ErrNoRows
	}
	return &task, err
}
func (s *recoveryCaptureStore) Tasks(_ context.Context, instance string, limit, offset int) ([]requestcapture.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []requestcapture.Task
	for _, raw := range s.tasks {
		var task requestcapture.Task
		if err := json.Unmarshal(raw, &task); err != nil {
			return nil, err
		}
		if task.InstanceID == instance {
			out = append(out, task)
		}
	}
	if offset >= len(out) {
		return nil, nil
	}
	return out[offset:min(len(out), offset+limit)], nil
}
func (s *recoveryCaptureStore) SaveRecord(_ context.Context, record *requestcapture.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(record)
	s.records[record.ID] = raw
	return err
}
func (s *recoveryCaptureStore) Record(_ context.Context, task, id string) (*requestcapture.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.records[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	var record requestcapture.Record
	err := json.Unmarshal(raw, &record)
	if record.TaskID != task {
		return nil, sql.ErrNoRows
	}
	return &record, err
}
func (s *recoveryCaptureStore) Records(_ context.Context, task, rid string, onlyErrors bool, limit, offset int) ([]requestcapture.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []requestcapture.Record
	for _, raw := range s.records {
		var record requestcapture.Record
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		if record.TaskID == task && (rid == "" || record.RequestID == rid) && (!onlyErrors || (record.IsError && record.FinishedAt != nil)) {
			out = append(out, record)
		}
	}
	if offset >= len(out) {
		return nil, nil
	}
	return out[offset:min(len(out), offset+limit)], nil
}
func (s *recoveryCaptureStore) DeleteRecord(_ context.Context, _, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, id)
	return nil
}
func (s *recoveryCaptureStore) DeleteTask(_ context.Context, _, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, id)
	return nil
}

func TestOpenAIWSRecoveryRefusalCapture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store := &recoveryCaptureStore{tasks: map[string][]byte{}, records: map[string][]byte{}}
	directory := t.TempDir()
	manager, err := requestcapture.New(store, directory, requestcapture.Config{Enabled: true, QuotaMiB: 8, RetentionDays: 1})
	require.NoError(t, err)
	defer manager.Close()
	task, err := manager.Create(ctx, requestcapture.CreateTask{TargetType: "user", TargetID: 26, DurationMinutes: 1}, "fixture user")
	require.NoError(t, err)
	session := manager.Begin(requestcapture.Meta{RequestID: "fixture-ws-recovery-capture", UserID: 26, GroupID: 2, Method: "GET", Path: "/v1/responses", Protocol: "websocket"})
	require.NotNil(t, session)
	done := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			done <- false
			return
		}
		defer conn.CloseNow()
		_, body, err := conn.Read(r.Context())
		if err != nil {
			t.Error(err)
			done <- false
			return
		}
		session.ClientFrame(body)
		handled := (&OpenAIGatewayHandler{}).handleOpenAIWSAgentRecoveryError(requestcapture.WithSession(r.Context(), session), conn, &service.OpenAIAgentRecoveryError{Reason: "owner_conflict"})
		session.Finish(http.StatusSwitchingProtocols)
		done <- handled
	}))
	defer server.Close()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	input := `{"type":"response.create","model":"gpt-5.1","input":[{"type":"function_call_output","call_id":"fixture-call","output":"fixture complete"}]}`
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(input)))
	_, wire, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(409), gjson.GetBytes(wire, "status").Int())
	_, _, err = conn.Read(ctx)
	require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
	select {
	case handled := <-done:
		require.True(t, handled)
	case <-ctx.Done():
		t.Fatal("refusal handler did not finish")
	}
	require.Eventually(t, func() bool { return manager.Stats().ActiveRequests == 0 }, 3*time.Second, 5*time.Millisecond,
		"capture worker must finalize before shutdown stops its task")
	records, err := manager.Records(context.Background(), task.ID, "", true, 10, 0)
	require.NoError(t, err)
	require.Len(t, records, 1, "RECOVERY_REFUSAL_CAPTURE_MISSING: a delivered WS error must survive errors-only retention")
	record := records[0]
	require.True(t, record.IsError)
	require.NotNil(t, record.FinishedAt)
	require.False(t, record.Partial, "capture should be complete: %s", record.Reason)
	require.Equal(t, "encrypted_agent_recovery_owner_conflict", record.ErrorCode)
	require.Empty(t, record.Attempts, "pre-dispatch refusal must not invent an upstream request")
	parts := map[string][]byte{}
	for _, part := range record.Parts {
		body, err := os.ReadFile(filepath.Join(directory, task.ID, record.ID, part.Name))
		require.NoError(t, err)
		parts[part.Stage] = body
	}
	require.Len(t, parts, 2)
	require.JSONEq(t, input, string(parts["client_request"]))
	require.Contains(t, string(parts["client_response"]), string(wire), "persist the actual delivered error frame")
}
