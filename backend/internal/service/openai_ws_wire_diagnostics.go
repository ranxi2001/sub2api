package service

import (
	"context"

	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Fixed field-presence/count metadata only. Never log response IDs, hashes,
// input text, tool content, headers, credentials or arbitrary JSON keys.
func openAIWSWireRefusal(ctx context.Context, stage string, source, payload []byte) error {
	sourceInput, wireInput := gjson.GetBytes(source, "input"), gjson.GetBytes(payload, "input")
	inputKind := "absent"
	switch {
	case sourceInput.IsArray():
		inputKind = "array"
	case sourceInput.Type == gjson.String:
		inputKind = "string"
	case sourceInput.Exists():
		inputKind = "other"
	}
	storeKind := "absent"
	switch gjson.GetBytes(source, "store").Type {
	case gjson.True:
		storeKind = "true"
	case gjson.False:
		storeKind = "false"
	default:
		if gjson.GetBytes(source, "store").Exists() {
			storeKind = "other"
		}
	}
	return openAIRecoveryWireRefusal(ctx, stage,
		zap.String("source_input_kind", inputKind), zap.String("source_store", storeKind),
		zap.Int("source_input_items", len(sourceInput.Array())), zap.Int("wire_input_items", len(wireInput.Array())),
		zap.Bool("source_previous_present", gjson.GetBytes(source, "previous_response_id").String() != ""),
		zap.Bool("wire_previous_present", gjson.GetBytes(payload, "previous_response_id").String() != ""),
		zap.Bool("source_model_present", gjson.GetBytes(source, "model").String() != ""),
		zap.Bool("input_changed", openAIAgentRecoveryInputDigest(source) != openAIAgentRecoveryInputDigest(payload)),
		zap.Bool("controls_changed", openAI409ControlsDigest(source) != openAI409ControlsDigest(payload)))
}
