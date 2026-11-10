//go:build unit

package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSSSEIncompleteAlreadyCommunicatedDoesNotAppendFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"json", "sse"} {
		t.Run(mode, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
			before := c.Writer.Size()
			if mode == "json" {
				c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
					"type": "upstream_error", "code": service.OpenAIWSSSEIncompleteCode,
					"message": "Upstream stream ended before completion",
				}})
			} else {
				c.Header("Content-Type", "text/event-stream")
				_, err := c.Writer.WriteString("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"type\":\"upstream_error\",\"code\":\"" + service.OpenAIWSSSEIncompleteCode + "\",\"message\":\"Upstream stream ended before completion\"}}}\n\n")
				require.NoError(t, err)
			}
			body := rec.Body.String()
			forwardErr := fmt.Errorf("forward: %w", &service.OpenAIWSSSEIncompleteError{
				Stage: "awaiting_terminal", CloseCode: 1000, EventCount: 2,
				RequestWritten: true, DownstreamCommitted: mode == "sse", ResponseWritten: true,
			})
			reported := openAIForwardErrorAlreadyCommunicated(c, before, forwardErr)
			require.True(t, reported, "a written typed response must not be replaced by the handler's generic error")
			if !reported {
				(&OpenAIGatewayHandler{}).ensureForwardErrorResponse(c, mode == "sse")
			}
			require.Equal(t, body, rec.Body.String())
			if mode == "json" {
				require.Equal(t, http.StatusBadGateway, rec.Code)
				require.True(t, json.Valid(rec.Body.Bytes()))
				require.NotContains(t, body, "event:")
			} else {
				require.Equal(t, 1, strings.Count(body, "event: response.failed\n"))
			}
		})
	}
}

func TestOpenAIWSSSEIncompleteUnwrittenResponseStillNeedsFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name           string
		priorBody      string
		writeMetadata  bool
		responseMarked bool
	}{
		{name: "no response", responseMarked: false},
		{name: "marked response without write", responseMarked: true},
		{name: "only earlier output", priorBody: ": keepalive\n\n", responseMarked: true},
		{name: "only new metadata", writeMetadata: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
			if tc.priorBody != "" {
				_, err := c.Writer.WriteString(tc.priorBody)
				require.NoError(t, err)
			}
			before := c.Writer.Size()
			if tc.writeMetadata {
				c.Header("Content-Type", "text/event-stream")
				_, err := c.Writer.WriteString("data: {\"type\":\"codex.response.metadata\"}\n\n")
				require.NoError(t, err)
			}
			forwardErr := &service.OpenAIWSSSEIncompleteError{
				Stage: "awaiting_terminal", CloseCode: 1000,
				RequestWritten: true, DownstreamCommitted: tc.writeMetadata,
				ResponseWritten: tc.responseMarked,
			}
			require.False(t, openAIForwardErrorAlreadyCommunicated(c, before, forwardErr), "neither metadata nor a flag without a new response proves the error was communicated")
			if tc.priorBody == "" && !tc.writeMetadata {
				require.True(t, (&OpenAIGatewayHandler{}).ensureForwardErrorResponse(c, false))
				require.Equal(t, http.StatusBadGateway, rec.Code)
				require.True(t, json.Valid(rec.Body.Bytes()), rec.Body.String())
			} else if tc.writeMetadata {
				require.True(t, (&OpenAIGatewayHandler{}).ensureForwardErrorResponse(c, true))
				require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed\n"))
			}
		})
	}
}
