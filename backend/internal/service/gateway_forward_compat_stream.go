package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// anthropicCompatDisconnectDrainInterval 是 gateway.stream_data_interval_timeout
// 未启用时，客户端断开后继续读取上游的读间隔上限，与该配置项的默认值一致。
var anthropicCompatDisconnectDrainInterval = 180 * time.Second

// anthropicCompatStream 是 Anthropic 上游经 chat/responses 兼容入口流式转发时的
// 上游读取状态。上游请求 ctx 与客户端解绑，客户端断开后调用方停止向下游写入、
// 继续读取上游以合并最终 usage。逐行读间隔受 gateway.stream_data_interval_timeout
// 限制；该配置未启用时，客户端断开后以 anthropicCompatDisconnectDrainInterval 限制。
type anthropicCompatStream struct {
	pump        *anthropicNativeLinePump
	body        io.Closer
	maxLineSize int
	logPrefix   string
	requestID   string
	// clientGone 是请求 ctx 的 Done，客户端断开后为 nil。
	clientGone <-chan struct{}

	clientDisconnected bool
	sawMessageStart    bool
	// sawTerminal requires message_stop; stop_reason and [DONE] are not sufficient.
	sawTerminal     bool
	frame           anthropicSSEFrameAssembler
	frameCount      int
	contentBytes    int
	semanticContent bool
	inputUsageSeen  bool
	outputUsageSeen bool
	usageSeen       bool
	protocolFailure string
	// upstreamErr* 记录上游流内的 error 事件；收到后调用方结束读取。
	upstreamErrType    string
	upstreamErrMessage string
	upstreamErrPayload string
	// readErr 是上游流结束的原因（io.EOF、读错误或 errAnthropicNativeStreamIdle）。
	readErr error
}

// anthropicCompatStreamFailure 描述客户端在线时上游流未正常结束的原因：
// err 返回给调用方，errType / message 写入下游的终止错误事件。
type anthropicCompatStreamFailure struct {
	err     error
	errType string
	message string
}

// newAnthropicCompatStream 启动上游行泵；调用方 defer stop()。
func newAnthropicCompatStream(cfg *config.Config, c *gin.Context, resp *http.Response, logPrefix, requestID string) *anthropicCompatStream {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Split(anthropicSSELineSplit())
	maxLineSize := defaultMaxLineSize
	if cfg != nil && cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	stream := &anthropicCompatStream{
		pump:        newAnthropicNativeLinePump(scanner, anthropicNativeStreamInterval(cfg)),
		body:        resp.Body,
		maxLineSize: maxLineSize,
		logPrefix:   logPrefix,
		requestID:   requestID,
	}
	if c.Request != nil {
		stream.clientGone = c.Request.Context().Done()
	}
	return stream
}

func (st *anthropicCompatStream) stop() {
	_ = st.body.Close()
	st.pump.stop()
}

// markClientDisconnected 标记客户端已断开，此后调用方不再向下游写入；读间隔上限
// 未启用时以 anthropicCompatDisconnectDrainInterval 启用。
func (st *anthropicCompatStream) markClientDisconnected() {
	if st.clientDisconnected {
		return
	}
	st.clientDisconnected = true
	st.clientGone = nil
	st.pump.enableInterval(anthropicCompatDisconnectDrainInterval)
	logger.L().Info(st.logPrefix+" stream: client disconnected, continuing to drain upstream for billing",
		zap.String("request_id", st.requestID),
	)
}

// nextLine 返回上游下一行；ok=false 表示上游流已结束，原因记入 readErr。
// 读间隔超时时关闭 body，解除泵 goroutine 阻塞中的读并释放上游连接。
// 上游流结束时请求 ctx 已取消的，按客户端已断开收尾。
func (st *anthropicCompatStream) nextLine() (string, bool) {
	for {
		if st.clientGone != nil {
			select {
			case <-st.clientGone:
				st.markClientDisconnected()
			default:
			}
		}
		line, err := st.pump.nextOrClientGone(st.clientGone)
		if err == nil {
			return line, true
		}
		if errors.Is(err, errAnthropicNativeClientGone) {
			st.markClientDisconnected()
			continue
		}
		if st.clientGone != nil {
			select {
			case <-st.clientGone:
				st.clientDisconnected = true
				st.clientGone = nil
			default:
			}
		}
		st.readErr = err
		switch {
		case errors.Is(err, errAnthropicNativeStreamIdle):
			_ = st.body.Close()
			logger.L().Warn(st.logPrefix+" stream: data interval timeout",
				zap.String("request_id", st.requestID),
				zap.Duration("interval", st.pump.interval),
				zap.Bool("client_disconnected", st.clientDisconnected),
			)
		case errors.Is(err, io.EOF), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			// 正常收流或取消：不记日志。
		default:
			logger.L().Warn(st.logPrefix+" stream: read error",
				zap.String("status", "read_error"),
				zap.String("request_id", st.requestID),
			)
		}
		return "", false
	}
}

// observe 记录决定收尾方式的上游事件。收到上游 error 事件时关闭上游 body，
// 调用方不再处理该事件并结束读取。
func (st *anthropicCompatStream) observe(event *apicompat.AnthropicStreamEvent, payload string) {
	// Inspect field presence before converters normalize zero-valued structs.
	usagePath := ""
	if event.Type == "message_start" {
		usagePath = "message.usage"
	}
	if event.Type == "message_delta" {
		usagePath = "usage"
	}
	if usagePath != "" {
		u := gjson.Get(payload, usagePath)
		st.usageSeen = st.usageSeen || anthropicUsageFieldsObserved(u)
		for _, key := range []string{"input_tokens", "prompt_tokens", "output_tokens"} {
			value := u.Get(key)
			if !value.Exists() || value.Type == gjson.Null {
				continue
			}
			if value.Type != gjson.Number || value.Float() < 0 || value.Float() != float64(value.Int()) {
				st.protocolFailure = "invalid Anthropic usage counter"
				continue
			}
			if key == "output_tokens" {
				st.outputUsageSeen = true
			} else {
				st.inputUsageSeen = true
			}
		}
	}
	if event.ContentBlock != nil {
		st.observeContent(*event.ContentBlock)
	}
	if event.Message != nil {
		for _, block := range event.Message.Content {
			st.observeContent(block)
		}
	}
	if event.Type == "content_block_delta" && event.Delta != nil {
		n := len(event.Delta.Text) + len(event.Delta.Thinking)
		st.contentBytes += n
		st.semanticContent = st.semanticContent || n > 0
	}

	switch event.Type {
	case "message_start":
		st.sawMessageStart = true
	case "message_stop":
		st.sawTerminal = true
	case "error":
		st.upstreamErrPayload = payload
		st.upstreamErrType = firstNonEmpty(gjson.Get(payload, "error.type").String(), "upstream_error")
		st.upstreamErrMessage = firstNonEmpty(sanitizeUpstreamErrorMessage(gjson.Get(payload, "error.message").String()), "upstream stream error")
		_ = st.body.Close()
		logger.L().Warn(st.logPrefix+" stream: upstream error event",
			zap.String("request_id", st.requestID),
			zap.String("status", "upstream_error"),
			zap.Int("frame_count", st.frameCount),
			zap.Bool("input_usage_seen", st.inputUsageSeen),
			zap.Bool("output_usage_seen", st.outputUsageSeen),
			zap.Bool("terminal_seen", st.sawTerminal),
			zap.Int("content_bytes", st.contentBytes),
			zap.Bool("client_disconnected", st.clientDisconnected),
		)
	}
}

// Availability is independent of completeness and value. Explicit zero and
// cache-only partial measurements are known usage and must remain billable.
func anthropicUsageFieldsObserved(u gjson.Result) bool {
	for _, path := range []string{"input_tokens", "prompt_tokens", "output_tokens", "cache_read_input_tokens", "cached_tokens", "prompt_tokens_details.cached_tokens", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens", "cache_creation_input_tokens", "cache_creation.ephemeral_5m_input_tokens", "cache_creation.ephemeral_1h_input_tokens"} {
		v := u.Get(path)
		if v.Type == gjson.Number && v.Float() >= 0 && v.Float() == float64(v.Int()) {
			return true
		}
	}
	return false
}

func (st *anthropicCompatStream) observeContent(block apicompat.AnthropicContentBlock) {
	n := len(block.Text) + len(block.Thinking) + len(block.Data)
	st.contentBytes += n
	st.semanticContent = st.semanticContent || n > 0 ||
		((block.Type == "tool_use" || block.Type == "server_tool_use") && block.ID != "" && block.Name != "")
}

// failure never treats a terminal or disconnect as proof of success. Known
// usage remains available to billing on all failures, without fabricating zero.
func (st *anthropicCompatStream) failure() *anthropicCompatStreamFailure {
	switch {
	case st.upstreamErrType != "":
		return &anthropicCompatStreamFailure{
			err:     &sseStreamErrorEventError{RawData: st.upstreamErrPayload},
			errType: st.upstreamErrType,
			message: st.upstreamErrMessage,
		}
	case st.clientDisconnected:
		return &anthropicCompatStreamFailure{err: fmt.Errorf("stream usage incomplete: client disconnected: %w", context.Canceled), errType: "client_disconnected", message: "client disconnected"}
	case errors.Is(st.readErr, errAnthropicNativeStreamIdle):
		return &anthropicCompatStreamFailure{
			err:     errAnthropicNativeStreamIdle,
			errType: "stream_timeout",
			message: fmt.Sprintf("upstream stream idle for %s", st.pump.interval),
		}
	case errors.Is(st.readErr, bufio.ErrTooLong), errors.Is(st.readErr, errAnthropicSSEFrameTooLarge):
		return &anthropicCompatStreamFailure{
			err:     fmt.Errorf("stream usage incomplete: %w", st.readErr),
			errType: "response_too_large",
			message: fmt.Sprintf("upstream SSE frame exceeded %d bytes", st.maxLineSize),
		}
	case st.readErr != nil && !errors.Is(st.readErr, io.EOF):
		return &anthropicCompatStreamFailure{
			err:     fmt.Errorf("stream usage incomplete: stream read error: %w", st.readErr),
			errType: "stream_read_error",
			message: "upstream stream disconnected: " + sanitizeStreamError(st.readErr),
		}
	case st.protocolFailure != "":
		return &anthropicCompatStreamFailure{err: errors.New("stream usage incomplete: " + st.protocolFailure), errType: "stream_protocol_error", message: st.protocolFailure}
	case !st.sawTerminal:
		return &anthropicCompatStreamFailure{
			err:     errors.New("stream usage incomplete: missing terminal event"),
			errType: "stream_incomplete",
			message: "upstream stream ended without a terminal event",
		}
	case !st.sawMessageStart || !st.semanticContent:
		return &anthropicCompatStreamFailure{err: errors.New("stream usage incomplete: missing semantic content"), errType: "stream_protocol_error", message: "upstream stream ended without semantic content"}
	case !st.inputUsageSeen || !st.outputUsageSeen:
		return &anthropicCompatStreamFailure{err: errors.New("stream usage incomplete: missing usage fields"), errType: "usage_incomplete", message: "upstream stream ended without complete usage"}
	default:
		return nil
	}
}

// Preserve partial usage on failure. Only the existing pre-output upstream
// rejection policy may fail over; incomplete/protocol errors never replay.
func finishAnthropicCompatStreamFailure(c *gin.Context, writerSizeBeforeStream int, failure *anthropicCompatStreamFailure, result *ForwardResult, writeFailure func()) (*ForwardResult, error) {
	var sseErr *sseStreamErrorEventError
	isSSEError := errors.As(failure.err, &sseErr)
	clientDisconnected := result.ClientDisconnect
	wroteFailure := false
	if !clientDisconnected && (!isSSEError || c.Writer.Size() != writerSizeBeforeStream || result.Usage.hasObservedTokens()) {
		writeFailure()
		wroteFailure = true
	}
	if !result.Usage.hasObservedTokens() {
		result = nil
	}
	if isAnthropicCompatSafetyRejection([]byte(extractCompatFailurePayload(failure.err))) {
		if !clientDisconnected && !wroteFailure {
			writeFailure()
		}
		return result, fmt.Errorf("upstream safety rejection: %s", failure.message)
	}
	return result, failure.err
}

// Gin records buffered render/write failures in c.Errors instead of returning
// them. Forwarders must propagate those failures while keeping measured usage.
func anthropicBufferedWriteFailure(c *gin.Context, stream *anthropicCompatStream, errorsBefore int) error {
	if len(c.Errors) <= errorsBefore {
		return nil
	}
	stream.markClientDisconnected()
	MarkResponseCommitted(c)
	return fmt.Errorf("stream usage incomplete: buffered response delivery failed: %w", c.Errors.Last().Err)
}

// writeAnthropicCompatCCStreamFailure 向 Chat Completions 流写出终止错误帧，
// 并标记响应已提交，handler 不再追加兜底错误。
func writeAnthropicCompatCCStreamFailure(c *gin.Context, failure *anthropicCompatStreamFailure) {
	payload, _ := json.Marshal(map[string]any{
		"error": map[string]string{
			"type":    failure.errType,
			"message": failure.message,
		},
	})
	commitAnthropicCompatStreamFailure(c, failure, "data: "+string(payload)+"\n\n")
}

// writeAnthropicCompatResponsesStreamFailure 向 Responses 流写出 response.failed
// 终止事件，沿用流内的 response id；sequence_number 经 restorer 与已写出的事件
// 保持连续。写出后标记响应已提交。
func writeAnthropicCompatResponsesStreamFailure(c *gin.Context, state *apicompat.AnthropicEventToResponsesState, restorer *apicompat.ResponsesClientToolStreamRestorer, failure *anthropicCompatStreamFailure) {
	responseID := state.ResponseID
	if responseID == "" {
		responseID = "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	evt := apicompat.ResponsesStreamEvent{
		Type:           "response.failed",
		SequenceNumber: state.SequenceNumber,
		Response: &apicompat.ResponsesResponse{
			ID:        responseID,
			Object:    "response",
			CreatedAt: state.Created,
			Model:     state.Model,
			Status:    "failed",
			Output:    []apicompat.ResponsesOutput{},
			Error:     &apicompat.ResponsesError{Code: failure.errType, Message: failure.message},
		},
	}
	state.SequenceNumber++
	payload, err := json.Marshal(evt)
	if err != nil {
		return
	}
	payloads, _, err := restorer.RestoreEvent(payload)
	if err != nil {
		return
	}
	var frames strings.Builder
	for _, restored := range payloads {
		fmt.Fprintf(&frames, "event: %s\ndata: %s\n\n", gjson.GetBytes(restored, "type").String(), restored)
	}
	commitAnthropicCompatStreamFailure(c, failure, frames.String())
}

func commitAnthropicCompatStreamFailure(c *gin.Context, failure *anthropicCompatStreamFailure, frame string) {
	MarkOpsStreamError(c, failure.errType, failure.message, http.StatusBadGateway)
	MarkResponseCommitted(c)
	if _, err := io.WriteString(c.Writer, frame); err != nil {
		return
	}
	c.Writer.Flush()
}

func extractCompatFailurePayload(err error) string {
	var sseErr *sseStreamErrorEventError
	if errors.As(err, &sseErr) {
		return sseErr.RawData
	}
	return ""
}

func isAnthropicCompatSafetyRejection(body []byte) bool {
	for _, key := range []string{"error.code", "error.type"} {
		switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, key).String())) {
		case "cyber_policy", "content_policy_violation", "content_policy_error", "safety_error", "safety_violation":
			return true
		}
	}
	message := strings.ToLower(gjson.GetBytes(body, "error.message").String())
	return strings.Contains(message, "this request was blocked by safe guard policy") || strings.Contains(message, "this request was blocked by safeguard policy")
}
