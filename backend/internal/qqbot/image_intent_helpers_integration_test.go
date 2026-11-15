//go:build integration

package qqbot

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"net/http"
	"strings"
	"testing"
)

// A mock protocol helper for existing execution tests; semantic accuracy is
// evaluated separately and is not established by these scripted responses.
func replyImageReviewTest(t *testing.T, w http.ResponseWriter, protocol string, body map[string]json.RawMessage) bool {
	t.Helper()
	if !strings.Contains(string(body["tools"]), imageReviewToolName) {
		return false
	}
	key := "messages"
	if protocol == "responses" {
		key = "input"
	}
	var messages []struct {
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(body[key], &messages))
	var data struct {
		Current string            `json:"current_message"`
		Pending *pendingImagePlan `json:"pending_plan"`
	}
	require.NoError(t, json.Unmarshal([]byte(messages[1].Content), &data))
	basis := "explicit"
	if data.Pending != nil {
		basis = "continuation"
	}
	_, _ = w.Write(imageReviewTestJSON(t, protocol, imageIntentReview{Disposition: "execute", Basis: basis, Evidence: data.Current}))
	return true
}
