package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const imageBillingResponseLimit int64 = 64 << 20

// Opt-in settlement responses are spooled to a private temporary file. They
// cannot expose a successful image/usage event until the ledger commits. The
// upstream stream is still drained normally; only comment heartbeats go out early.
type imageBillingResponse struct {
	gin.ResponseWriter
	file         *os.File
	header       http.Header
	status, size int
	stream       bool
	flushed      bool
	failure      error
}

func newImageBillingResponse(writer gin.ResponseWriter, stream bool) (*imageBillingResponse, error) {
	file, err := os.CreateTemp("", "sub2api-image-settlement-*")
	if err != nil {
		return nil, err
	}
	// Unlink on Unix while retaining the open descriptor: a process crash cannot
	// leave generated images behind. Other platforms still remove it on close.
	_ = os.Remove(file.Name())
	return &imageBillingResponse{ResponseWriter: writer, file: file, header: writer.Header().Clone(), status: 200, size: -1, stream: stream}, nil
}
func (w *imageBillingResponse) Header() http.Header { return w.header }
func (w *imageBillingResponse) Status() int         { return w.status }
func (w *imageBillingResponse) Size() int           { return w.size }
func (w *imageBillingResponse) Written() bool       { return w.size >= 0 }
func (w *imageBillingResponse) WriteHeader(code int) {
	if !w.Written() {
		w.status = code
	}
}
func (w *imageBillingResponse) WriteHeaderNow() {
	if w.size < 0 {
		w.size = 0
	}
}
func (w *imageBillingResponse) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *imageBillingResponse) Write(b []byte) (int, error) {
	w.WriteHeaderNow()
	if int64(w.size)+int64(len(b)) > imageBillingResponseLimit {
		w.failure = errors.New("image billing response exceeds 64 MiB")
		return 0, w.failure
	}
	n, err := w.file.Write(b)
	w.size += n
	if err != nil {
		w.failure = err
	}
	return n, err
}
func (w *imageBillingResponse) Flush() {
	// Never expose a provisional amount or image as a successful completion.
	if !w.stream {
		return
	}
	w.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
	w.ResponseWriter.Header().Set("Cache-Control", "no-cache")
	w.ResponseWriter.Header().Set("X-Accel-Buffering", "no")
	_, _ = w.ResponseWriter.WriteString(": billing_pending\n\n")
	w.ResponseWriter.Flush()
}
func (w *imageBillingResponse) close() { _ = w.file.Close(); _ = os.Remove(w.file.Name()) }
func (w *imageBillingResponse) commitHeaders() {
	for k, v := range w.header {
		w.ResponseWriter.Header()[k] = v
	}
	w.ResponseWriter.Header().Del("Content-Length")
	w.ResponseWriter.Header().Del("ETag")
	w.ResponseWriter.WriteHeader(w.status)
}
func (w *imageBillingResponse) passthrough() {
	if w.flushed {
		return
	}
	w.flushed = true
	if w.status < http.StatusBadRequest {
		w.errorResponse("image_billing_unavailable", "Image settlement was not confirmed; do not automatically retry")
		return
	}
	if w.failure != nil {
		w.errorResponse("image_response_unavailable", "Image response could not be retained")
		return
	}
	w.commitHeaders()
	_, _ = w.file.Seek(0, io.SeekStart)
	_, _ = io.Copy(w.ResponseWriter, w.file)
	w.ResponseWriter.Flush()
}
func (w *imageBillingResponse) errorResponse(code, message string) {
	w.flushed = true
	payload, _ := json.Marshal(gin.H{"error": gin.H{"type": "api_error", "code": code, "message": message}})
	if w.stream {
		w.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w.ResponseWriter, "event: error\ndata: %s\n\n", payload)
	} else {
		w.ResponseWriter.Header().Set("Content-Type", "application/json")
		w.ResponseWriter.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.ResponseWriter.Write(payload)
	}
	w.ResponseWriter.Flush()
}
func imageEquivalentPayload(body []byte, receipt service.ImageBillingReceipt, includeUsage bool) ([]byte, error) {
	var out map[string]json.RawMessage
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("invalid image response")
	}
	delete(out, "billing")
	delete(out, "upstream_usage")
	if raw, ok := out["usage"]; ok {
		out["upstream_usage"] = raw
		delete(out, "usage")
	}
	if includeUsage {
		tokens, err := receipt.EquivalentTokens()
		if err != nil {
			return nil, err
		}
		out["usage"], _ = json.Marshal(gin.H{"input_tokens": 0, "output_tokens": tokens, "total_tokens": tokens})
		out["billing"], _ = json.Marshal(gin.H{"usage_kind": "billing_equivalent", "billing_mode": receipt.BillingMode, "actual_cost": receipt.ActualCost, "image_count": receipt.ImageCount, "output_price_per_million": service.ImageEquivalentOutputPricePerMillion, "minimum_charge": receipt.MinimumCharge, "minimum_charge_adjustment": receipt.MinimumChargeAdjustment})
	}
	return json.Marshal(out)
}
func (w *imageBillingResponse) settle(receipt service.ImageBillingReceipt) error {
	if w.failure != nil {
		return w.failure
	}
	if _, err := receipt.EquivalentTokens(); err != nil {
		return err
	}
	_, _ = w.file.Seek(0, io.SeekStart)
	body, err := io.ReadAll(io.LimitReader(w.file, imageBillingResponseLimit+1))
	if err != nil {
		return err
	}
	var out []byte
	if !w.stream {
		out, err = imageEquivalentPayload(body, receipt, true)
	} else {
		// Rewrite only JSON data events. Put the request total on the final image
		// event exactly once, including multi-image and partial-success responses.
		blocks := bytes.Split(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")), []byte("\n\n"))
		payloads := make([][]byte, len(blocks))
		last := -1
		for i, block := range blocks {
			var data [][]byte
			for _, line := range bytes.Split(block, []byte("\n")) {
				if bytes.HasPrefix(line, []byte("data:")) {
					data = append(data, bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" ")))
				}
			}
			payload := bytes.Join(data, []byte("\n"))
			var event map[string]json.RawMessage
			if json.Unmarshal(payload, &event) != nil || event == nil {
				continue
			}
			payloads[i] = payload
			var kind string
			_ = json.Unmarshal(event["type"], &kind)
			if strings.HasSuffix(kind, ".completed") || event["b64_json"] != nil || event["url"] != nil || event["data"] != nil {
				last = i
			}
		}
		if last < 0 {
			return errors.New("image stream has no completed image event")
		}
		for i, payload := range payloads {
			if payload == nil {
				continue
			}
			rewritten, e := imageEquivalentPayload(payload, receipt, i == last)
			if e != nil {
				return e
			}
			var lines [][]byte
			for _, line := range bytes.Split(blocks[i], []byte("\n")) {
				if !bytes.HasPrefix(line, []byte("data:")) {
					lines = append(lines, line)
				}
			}
			lines = append(lines, append([]byte("data: "), rewritten...))
			blocks[i] = bytes.Join(lines, []byte("\n"))
		}
		out = bytes.Join(blocks, []byte("\n\n"))
	}
	if err != nil {
		return err
	}
	w.flushed = true
	w.commitHeaders()
	_, err = w.ResponseWriter.Write(out)
	w.ResponseWriter.Flush()
	return err
}
