package requestcapture

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/Wei-Shaw/sub2api/internal/util/transportdiag"
	"github.com/tidwall/gjson"
)

// WSRequest begins one actual upstream frame/attempt. RawMessage avoids an extra
// encoding allocation. Other values must reserve a conservative encoding bound.
func (s *Session) WSRequest(account int64, value any) int {
	return s.WSRequestWithRoute(account, value, transportdiag.Route{})
}

func (s *Session) WSRequestWithRoute(account int64, value any, route transportdiag.Route) int {
	if s == nil {
		return 0
	}
	route.Upstream = "websocket"
	route.ResponseFormat = "websocket_frames"
	n := s.BeginRoutedAttempt(account, route)
	var payload []byte
	switch v := value.(type) {
	case json.RawMessage:
		payload = v
	case []byte:
		payload = v
	default:
		charge := jsonBound(reflect.ValueOf(value), 0)
		if charge > BufferLimit || !s.m.reserve(charge) {
			s.MarkPartial("serialization_buffer_limit")
			return n
		}
		defer s.m.buffer.Add(-charge)
		var err error
		payload, err = json.Marshal(value)
		if err != nil {
			s.MarkPartial("serialization_failed")
			return n
		}
	}
	s.Frame("upstream_request", n, payload)
	return n
}
func (s *Session) LastAttempt() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.attempts)
}

// WebSocket response frames are stored as SSE data events in one stage/turn
// segment, preserving frame order without a separate file per delta.
func (s *Session) Frame(stage string, attempt int, payload []byte) {
	if s == nil {
		return
	}
	if stage != "upstream_response" && stage != "client_response" {
		stream := s.NewStream(stage, attempt, 0, "application/json", nil)
		_, _ = stream.Write(payload)
		_ = stream.Close()
		return
	}
	s.frameMu.Lock()
	defer s.frameMu.Unlock()
	s.frameLocked(stage, attempt, payload)
}

// Commit a successfully written client frame before the next inbound turn can
// reset its capture state. The actual write retains the caller's timeout.
func (s *Session) DeliverClientFrame(payload []byte, write func() error) error {
	if s == nil {
		return write()
	}
	s.frameMu.Lock()
	defer s.frameMu.Unlock()
	err := write()
	if err != nil {
		s.MarkPartial("client_write_failed")
		s.MarkError("client_write_failed")
		return err
	}
	s.frameLocked("client_response", 0, payload)
	return nil
}

func (s *Session) frameLocked(stage string, attempt int, payload []byte) {
	s.mu.Lock()
	turn := s.turn
	closed := s.closed || s.failed || s.turnEnded
	s.mu.Unlock()
	if closed {
		return
	}
	if s.frameStreams == nil {
		s.frameStreams = map[string]*Stream{}
	}
	stream := s.frameStreams[stage]
	if stream != nil && (stream.part.Attempt != attempt || stream.part.Turn != turn) {
		_ = stream.Close()
		stream = nil
	}
	if stream == nil {
		stream = s.NewStream(stage, attempt, turn, "text/event-stream; profile=websocket-frames", nil)
		s.frameStreams[stage] = stream
	}
	terminal := isTerminalFrame(payload)
	for len(payload) > 0 {
		line, rest, found := bytes.Cut(payload, []byte{'\n'})
		_, _ = stream.Write([]byte("data: "))
		_, _ = stream.Write(line)
		_, _ = stream.Write([]byte("\n"))
		payload = rest
		if !found {
			break
		}
	}
	_, _ = stream.Write([]byte("\n"))
	if stage == "client_response" && terminal {
		s.endTurnLocked()
	}
}
func isTerminalFrame(payload []byte) bool {
	switch gjson.GetBytes(payload, "type").String() {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "error":
		return true
	}
	return false
}

// Called under frameMu after the terminal client frame has been enqueued.
func (s *Session) endTurnLocked() {
	s.mu.Lock()
	isWS := s.meta.Protocol == "websocket"
	s.mu.Unlock()
	// HTTP bridges finish at the HTTP response boundary.
	if !isWS {
		return
	}
	for key, stream := range s.frameStreams {
		_ = stream.Close()
		delete(s.frameStreams, key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.failed || s.turnEnded {
		return
	}
	const charge = int64(64 << 10)
	if !s.m.reserve(charge) {
		s.failed = true
		s.reason = "buffer_limit"
		s.m.signal()
		return
	}
	snapshot := &Record{}
	s.snapshotLocked(snapshot)
	snapshot.Status = 101
	targets := []string{}
	for _, t := range s.candidates {
		if s.matched[t.task.ID] && !s.finishedTargets[t.task.ID] {
			targets = append(targets, t.task.ID)
		}
	}
	s.pending.Add(1)
	select {
	case s.m.queue <- event{s: s, final: snapshot, targets: targets, charge: charge}:
		s.turnEnded = true
		s.releaseInputLocked()
	default:
		s.pending.Add(-1)
		s.m.buffer.Add(-charge)
		s.failed = true
		s.reason = "queue_full"
		s.m.signal()
	}
}
func (s *Session) closeFrameStreams() {
	s.frameMu.Lock()
	defer s.frameMu.Unlock()
	for key, stream := range s.frameStreams {
		_ = stream.Close()
		delete(s.frameStreams, key)
	}
}
func jsonBound(v reflect.Value, depth int) int64 {
	if depth > 128 {
		return BufferLimit + 1
	}
	if !v.IsValid() {
		return 16
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return 16
		}
		return jsonBound(v.Elem(), depth+1)
	case reflect.String:
		return int64(v.Len())*6 + 16
	case reflect.Map:
		n := int64(64)
		it := v.MapRange()
		for it.Next() {
			n += jsonBound(it.Key(), depth+1) + jsonBound(it.Value(), depth+1)
			if n > BufferLimit {
				return n
			}
		}
		return n * 2
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return int64(v.Len())*8 + 64
		}
		n := int64(64)
		for i := 0; i < v.Len(); i++ {
			n += jsonBound(v.Index(i), depth+1)
			if n > BufferLimit {
				return n
			}
		}
		return n * 2
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return 64
	default:
		return BufferLimit + 1
	}
}
