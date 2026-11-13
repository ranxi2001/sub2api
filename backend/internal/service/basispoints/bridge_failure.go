package basispoints

// BridgeFailure describes local conversion failure, never an upstream HTTP
// rejection. Only bounded structural metadata may be added to Details.
type BridgeFailure struct {
	Message               string
	FailureStage          string
	RejectedTool          string
	RepairSkippedReason   string
	UpstreamTerminalEvent string
	ToolArguments         *ToolArgumentsDiagnostic
}

func (f *BridgeFailure) Details() map[string]any {
	result := object{"type": "server_error", "code": "basispoints_protocol_error", "message": f.Message, "failure_stage": f.FailureStage}
	if f.RejectedTool != "" {
		result["rejected_tool"] = f.RejectedTool
	}
	if f.RepairSkippedReason != "" {
		result["repair_skipped_reason"] = f.RepairSkippedReason
	}
	if f.UpstreamTerminalEvent != "" {
		result["upstream_terminal_event"] = f.UpstreamTerminalEvent
	}
	if f.ToolArguments != nil {
		result["tool_arguments"] = f.ToolArguments
	}
	return result
}

// ParseBridgeFailure consumes the bridge's normalized output. Raw upstream
// failures pass through classifyUpstreamFailure first, which drops these fields.
func ParseBridgeFailure(raw []byte) *BridgeFailure {
	var payload object
	if decode(raw, &payload) != nil || text(payload["type"]) != "response.failed" {
		return nil
	}
	response, _ := payload["response"].(object)
	detail, _ := response["error"].(object)
	stage := text(detail["failure_stage"])
	if text(detail["code"]) != "basispoints_protocol_error" || (stage != "bridge_tool_validation" && stage != "bridge_protocol_conversion") {
		return nil
	}
	failure := &BridgeFailure{Message: text(detail["message"]), FailureStage: stage,
		RejectedTool: safeSchemaName(text(detail["rejected_tool"])), RepairSkippedReason: text(detail["repair_skipped_reason"]),
		UpstreamTerminalEvent: text(detail["upstream_terminal_event"])}
	if d, ok := detail["tool_arguments"].(object); ok {
		reason := text(d["reason"])
		if reason == "schema_validation" || reason == "resource_limit" {
			failure.ToolArguments = &ToolArgumentsDiagnostic{Tool: safeSchemaName(text(d["tool"])), Reason: reason,
				Path: safeToolArgumentPath(text(d["path"])), Rule: safeToolArgumentRule(text(d["rule"]))}
		}
	}
	return failure
}
