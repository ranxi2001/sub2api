package service

// OpenAIRecoverySubreason limits persisted diagnostics to fixed categories.
// Kept independent of request/proof types so historical error callers remain coherent.
func OpenAIRecoverySubreason(value string) string { return safeOpenAIRecoverySubreason(value) }

func safeOpenAIRecoverySubreason(value string) string {
	switch value {
	case "tool_provenance_unknown", "tool_provenance_unavailable", "tool_provenance_changed", "tool_provenance_task_changed", "mixed_completed_owners", "owner_record_conflicted", "owner_record_pending", "owner_record_failed",
		"owner_delivery_unknown", "owner_route_changed", "owner_record_changed", "owner_lookup_unavailable",
		"tool_invalid", "tool_incomplete", "tool_duplicate", "tool_unpaired", "tool_history_unsupported",
		"shadow_record_changed", "shadow_epoch_changed", "shadow_fence_changed", "shadow_fence_invalid",
		"shadow_store_unavailable", "input_invalid", "input_integrity", "controls_integrity", "readable_intent_missing",
		"http_projection_source", "apikey_projection_source", "oauth_projection_source",
		"ws_body_unreadable", "ws_readable_source", "ws_projection_integrity", "ws_previous_removed":
		return value
	default:
		return "unknown"
	}
}
