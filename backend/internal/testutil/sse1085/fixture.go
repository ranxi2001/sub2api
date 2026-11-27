//go:build unit && issue1085

package sse1085

import (
	"context"
	"io"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// Issue1085Body gates the terminal on the first successful delta flush. For
// native async readers EOF also proves that all terminal/trailing lines have
// passed the scanner's sends into its bounded queue (fewer than 16 lines).
type Issue1085Body struct {
	first, tail *strings.Reader
	started     chan struct{}
	queued      chan struct{}
	closed      chan struct{}
	startOnce   sync.Once
	queueOnce   sync.Once
	closeOnce   sync.Once
}

func NewIssue1085Body(first, tail string) *Issue1085Body {
	return &Issue1085Body{first: strings.NewReader(first), tail: strings.NewReader(tail),
		started: make(chan struct{}), queued: make(chan struct{}), closed: make(chan struct{})}
}

func (body *Issue1085Body) Read(p []byte) (int, error) {
	if body.first.Len() > 0 {
		return body.first.Read(p)
	}
	select {
	case <-body.started:
	case <-body.closed:
		return 0, io.ErrClosedPipe
	}
	if body.tail.Len() > 0 {
		return body.tail.Read(p)
	}
	body.queueOnce.Do(func() { close(body.queued) })
	return 0, io.EOF
}

func (body *Issue1085Body) Close() error {
	body.closeOnce.Do(func() { close(body.closed) })
	return nil
}

// Issue1085Writer fails only the terminal, after a delta has flushed. It is
// installed inside Gin so the real Ops capture writer delegates through it.
type Issue1085Writer struct {
	gin.ResponseWriter
	Body           *Issue1085Body
	Context        context.Context
	Native         bool
	Failure        string
	WrittenBody    strings.Builder
	DeltaFlushed   bool
	TerminalWrite  bool
	TerminalFlush  bool
	QueueFilled    bool
	QueueWaitError error
}

func (writer *Issue1085Writer) Write(p []byte) (int, error) {
	writer.WrittenBody.Write(p)
	if strings.Contains(writer.WrittenBody.String(), "response.failed") {
		writer.TerminalWrite = true
		if writer.Failure == "write" {
			return 0, io.ErrClosedPipe
		}
	}
	return writer.ResponseWriter.Write(p)
}

func (writer *Issue1085Writer) WriteString(value string) (int, error) {
	return writer.Write([]byte(value))
}

func (writer *Issue1085Writer) FlushError() error {
	if writer.TerminalWrite {
		writer.TerminalFlush = true
		if writer.Failure == "flush" {
			return io.ErrClosedPipe
		}
	}
	if err := flushIssue1085Writer(writer.ResponseWriter); err != nil {
		return err
	}
	if !writer.DeltaFlushed && strings.Contains(writer.WrittenBody.String(), "response.output_text.delta") {
		writer.DeltaFlushed = true
		writer.Body.startOnce.Do(func() { close(writer.Body.started) })
		if writer.Native {
			// No sleeps: hold the consumer while the scanner queues the failed
			// terminal AND trailing frame. Its blank boundary cannot drain it.
			select {
			case <-writer.Body.queued:
				writer.QueueFilled = true
			case <-writer.Context.Done():
				writer.QueueWaitError = writer.Context.Err()
				return writer.QueueWaitError
			}
		}
	}
	return nil
}

func flushIssue1085Writer(writer gin.ResponseWriter) error {
	writer.WriteHeaderNow()
	writer.Flush()
	return nil
}
