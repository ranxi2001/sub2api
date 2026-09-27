package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const nativeCodexImageBridgeKey = "sub2api_native_codex_image_bridge"

type nativeCodexImageBridge struct {
	bridge *basispoints.Bridge
	state  *excelBPSImageGenerationState
	cancel context.CancelFunc
}

func (s *OpenAIGatewayService) prepareNativeCodexImageBridge(ctx context.Context, c *gin.Context, account *Account, body []byte, scope string) ([]byte, *nativeCodexImageBridge, error) {
	// Only the HTTP native OAuth desktop route opts into this compatibility
	// adapter. API-key providers, SDK adapters, compact, and explicit client
	// image tools retain their existing contracts.
	if !account.IsOpenAIOAuthLike() || account.IsCopilotSDKEnabled() ||
		GetOpenAIClientTransport(c) != OpenAIClientTransportHTTP || isOpenAIResponsesCompactPath(c) ||
		openAIRequestBodyHasImageGenerationDeclaration(body) {
		return body, nil, nil
	}
	imageCtx, cancel := context.WithCancel(ctx)
	state := &excelBPSImageGenerationState{}
	generate := s.excelBPSImageGenerator(imageCtx, c, account, gjson.GetBytes(body, "model").String(), state)
	updated, bridge, err := basispoints.PrepareNativeImageBridge(body, scope, generate)
	if err != nil || bridge == nil {
		cancel()
		return updated, nil, err
	}
	adapter := &nativeCodexImageBridge{bridge: bridge, state: state, cancel: cancel}
	c.Set(nativeCodexImageBridgeKey, adapter)
	return updated, adapter, nil
}

func nativeCodexImageBridgeFor(c *gin.Context) *nativeCodexImageBridge {
	value, _ := c.Get(nativeCodexImageBridgeKey)
	adapter, _ := value.(*nativeCodexImageBridge)
	return adapter
}

func applyNativeCodexImageResponse(ctx context.Context, c *gin.Context, response *http.Response) error {
	adapter := nativeCodexImageBridgeFor(c)
	if adapter == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil
	}
	if isEventStreamResponse(response.Header) {
		response.Body = adapter.bridge.NativeImageStream(ctx, response.Body, adapter.cancel)
	} else {
		original := response.Body
		data, err := io.ReadAll(io.LimitReader(original, (32<<20)+1))
		_ = original.Close() // release the parent lease before image generation
		if err != nil {
			return err
		}
		if len(data) > 32<<20 {
			return fmt.Errorf("native image bridge JSON response exceeds 32 MiB")
		}
		data, err = adapter.bridge.NativeImageJSON(data)
		if err != nil {
			return err
		}
		response.Body = io.NopCloser(bytes.NewReader(data))
	}
	response.ContentLength = -1
	response.Header.Del("Content-Length")
	return nil
}
