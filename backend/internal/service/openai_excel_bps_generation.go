package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type excelBPSImageSlotKey struct{}

// WithExcelBPSImageSlotAcquirer defers the existing handler image limit until
// the model actually selects the server-owned tool. Ordinary chat holds no slot.
func WithExcelBPSImageSlotAcquirer(ctx context.Context, acquire func(context.Context) (func(), bool)) context.Context {
	return context.WithValue(ctx, excelBPSImageSlotKey{}, acquire)
}

type excelBPSImageGenerationState struct {
	mu     sync.Mutex
	result *OpenAIForwardResult
}

func (s *OpenAIGatewayService) excelBPSImageGenerator(ctx context.Context, c *gin.Context, account *Account, model string, state *excelBPSImageGenerationState) basispoints.ImageGenerator {
	apiKey := getAPIKeyFromContext(c)
	if apiKey == nil || !GroupAllowsImageGeneration(apiKey.Group) ||
		isOpenAIResponsesCompactPath(c) ||
		isCodexSparkModel(account.GetMappedModel(model)) ||
		account.CodexImageGenerationExplicitToolPolicy() == codexImageGenerationExplicitToolPolicyStrip ||
		!s.isCodexImageGenerationBridgeEnabled(ctx, account, apiKey) ||
		!(openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator")) || (s.cfg != nil && s.cfg.Gateway.ForceCodexCLI)) {
		return nil
	}
	acquire, _ := ctx.Value(excelBPSImageSlotKey{}).(func(context.Context) (func(), bool))
	// Entry points without the handler's limiter must not bypass an enabled limit.
	if acquire == nil && s.cfg != nil && s.cfg.Gateway.ImageConcurrency.Enabled {
		return nil
	}
	// BPS lowers client tools to its own protocol, including Responses Lite
	// clients. The server-owned tool can therefore be offered on this path.
	// Its child request is a fresh, non-Lite native hosted-image request; the
	// native Lite passthrough restrictions elsewhere remain authoritative.
	snapshot := c.Copy()
	return func(request basispoints.ImageGenerationRequest) (basispoints.ImageGenerationResult, error) {
		imageCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()
		if err := imageCtx.Err(); err != nil {
			return basispoints.ImageGenerationResult{}, err
		}
		if acquire != nil {
			release, ok := acquire(imageCtx)
			if !ok {
				return basispoints.ImageGenerationResult{}, errors.New("image generation concurrency limit exceeded")
			}
			if release != nil {
				defer release()
			}
		}
		body, err := json.Marshal(map[string]any{
			"model": model, "stream": true, "store": false,
			"instructions": "Generate exactly one image matching the user's prompt using the image_generation tool.",
			"input":        request.Prompt,
			"tools":        []any{map[string]any{"type": "image_generation", "size": request.Size, "quality": request.Quality, "output_format": "png"}},
			"tool_choice":  map[string]any{"type": "image_generation"},
		})
		if err != nil {
			return basispoints.ImageGenerationResult{}, err
		}
		w := &excelBPSImageRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
		child, _ := gin.CreateTestContext(w)
		child.Keys = snapshot.Keys
		// This is a new HTTP image request, not a replay of the outer text
		// request. Reinitialize its permission and billing classification.
		SetOpenAIClientTransport(child, OpenAIClientTransportHTTP)
		SetOpenAIImageIntentHint(child, true)
		child.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(imageCtx)
		child.Request.Header.Set("Content-Type", "application/json")
		child.Request.Header.Set("User-Agent", snapshot.GetHeader("User-Agent"))
		child.Request.Header.Set("originator", snapshot.GetHeader("originator"))
		// The explicit hosted declaration routes this one call natively. No BPS
		// flag is changed, and Forward retains native auth, proxy and account policy.
		forward, forwardErr := s.Forward(imageCtx, child, account, body)
		state.mu.Lock()
		state.result = forward
		state.mu.Unlock()
		var response map[string]any
		responseBytes := w.Body.Bytes()
		if !gjson.ValidBytes(responseBytes) {
			_, terminal, ok := extractOpenAISSETerminalEvent(w.Body.String())
			if !ok {
				return basispoints.ImageGenerationResult{}, errors.New("native image stream has no terminal response")
			}
			responseBytes = []byte(gjson.GetBytes(terminal, "response").Raw)
		}
		decoder := json.NewDecoder(bytes.NewReader(responseBytes))
		decoder.UseNumber()
		decodeErr := decoder.Decode(&response)
		generated := basispoints.ImageGenerationResult{}
		if response != nil {
			generated.Usage, _ = response["usage"].(map[string]any)
			generated.ToolUsage, _ = response["tool_usage"].(map[string]any)
		}
		if forwardErr != nil || w.overflow || decodeErr != nil || w.Code != http.StatusOK || response["status"] != "completed" {
			return generated, errors.New("native image generation did not complete")
		}
		output, _ := response["output"].([]any)
		for _, raw := range output {
			item, _ := raw.(map[string]any)
			if item["type"] == "image_generation_call" && item["status"] == "completed" {
				if generated.Item != nil {
					return generated, errors.New("native generation returned multiple images")
				}
				generated.Item = item
			}
		}
		if generated.Item == nil {
			return generated, errors.New("native generation returned no completed image")
		}
		return generated, nil
	}
}

func (s *excelBPSImageGenerationState) apply(result *OpenAIForwardResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.result == nil || result == nil {
		return
	}
	native := s.result
	result.ImageCount, result.ImageSize = native.ImageCount, native.ImageSize
	result.ImageInputSize, result.ImageOutputSize = native.ImageInputSize, native.ImageOutputSize
	result.ImageOutputSizes, result.ImageSizeSource = native.ImageOutputSizes, native.ImageSizeSource
	result.ImageSizeBreakdown = native.ImageSizeBreakdown
	if native.ImageCount > 0 {
		result.BillingModel = native.BillingModel
	}
}

type excelBPSImageRecorder struct {
	*httptest.ResponseRecorder
	cancel   context.CancelFunc
	overflow bool
}

func (w *excelBPSImageRecorder) Write(data []byte) (int, error) {
	// Native SSE can contain the image in both output_item.done and completed.
	if w.overflow || w.Body.Len()+len(data) > 32<<20 {
		w.overflow = true
		w.cancel()
		return 0, io.ErrShortWrite
	}
	return w.ResponseRecorder.Write(data)
}
func (w *excelBPSImageRecorder) WriteString(data string) (int, error) { return w.Write([]byte(data)) }
