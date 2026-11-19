package service

import (
	"context"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Diagnostic values are deliberately separate from authorization evidence.
// No body, item/call identity, route, hash, credential or store error belongs here.
type openAIRecoveryOwnerCounts struct {
	observed                                                           bool
	total, completed, pending, failed, conflicted, unknown, unexamined int
}

func (d *openAIRecoveryOwnerCounts) observe(b openAIAgentRecoveryBinding, found bool, err error) {
	d.unexamined--
	switch {
	case b.conflicted:
		d.conflicted++
	case b.pending != "":
		d.pending++
	case b.failed:
		d.failed++
	case err != nil || !found:
		d.unknown++
	default:
		d.completed++
	}
}

type openAIRecoveryToolCounts struct {
	observed                                                        bool
	calls, results, pairs, unpaired, duplicate, incomplete, invalid int
}

func inspectOpenAIWSCompletedTools(body []byte) (openAIRecoveryToolCounts, string) {
	d := openAIRecoveryToolCounts{observed: true}
	calls, results := map[string]string{}, map[string]string{}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		kind, id := item.Get("type").String(), item.Get("call_id").String()
		switch kind {
		case "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output", "tool_search_call", "tool_search_output":
			if id == "" || item.Get("call_id").Type != gjson.String {
				d.invalid++
			}
			if item.Get("status").Exists() && item.Get("status").String() != "completed" {
				d.incomplete++
			}
			if strings.HasSuffix(kind, "_output") {
				d.results++
				if results[id] != "" {
					d.duplicate++
				}
				// Discovery can carry callable schemas instead of a text result.
				hasOutput := item.Get("output").Exists() && item.Get("output").Type != gjson.Null
				if !hasOutput && (kind != "tool_search_output" || !item.Get("tools").IsArray()) {
					d.invalid++
				}
				results[id] = kind
			} else {
				d.calls++
				if calls[id] != "" {
					d.duplicate++
				}
				calls[id] = kind + "_output"
				if kind == "tool_search_call" {
					calls[id] = "tool_search_output"
				}
			}
		}
	}
	for id, kind := range calls {
		if results[id] == kind && id != "" {
			d.pairs++
		} else {
			d.unpaired++
		}
	}
	for id, kind := range results {
		if calls[id] != kind || id == "" {
			d.unpaired++
		}
	}
	switch {
	case d.invalid > 0:
		return d, "tool_invalid"
	case d.incomplete > 0:
		return d, "tool_incomplete"
	case d.duplicate > 0:
		return d, "tool_duplicate"
	case d.unpaired > 0:
		return d, "tool_unpaired"
	default:
		return d, ""
	}
}

type openAIRecoveryShadowStateKey struct{}
type openAIRecoveryShadowState struct{ lookup, epoch string }
type openAIRecoveryDiagnostics struct {
	subreason string
	owners    openAIRecoveryOwnerCounts
	tools     openAIRecoveryToolCounts
	shadow    openAIRecoveryShadowState
	controls  *OpsRecoveryControls
}

func openAIRecoveryDiagnosticError(reason, detail string, r *openAIAgentRecoveryRequest) error {
	e := &OpenAIAgentRecoveryError{Reason: reason}
	d := openAIRecoveryDiagnostics{}
	if r != nil {
		d = r.diagnostics
	}
	d.subreason = detail
	e.diagnostic = &d
	return e
}

func withOpenAIRecoveryDiagnostics(err error, r *openAIAgentRecoveryRequest, detail string) error {
	var recovery *OpenAIAgentRecoveryError
	if !errors.As(err, &recovery) {
		return err
	}
	copy := *recovery
	d := openAIRecoveryDiagnostics{}
	if r != nil {
		d = r.diagnostics
	}
	if recovery.diagnostic != nil && detail == "" {
		d.subreason = recovery.diagnostic.subreason
	} else {
		d.subreason = detail
	}
	if recovery.diagnostic != nil && recovery.diagnostic.shadow.lookup != "" {
		d.shadow = recovery.diagnostic.shadow
	}
	copy.diagnostic = &d
	return &copy
}

func openAIRecoveryOwnerDetail(b openAIAgentRecoveryBinding, found bool) string {
	switch {
	case b.conflicted:
		return "owner_record_conflicted"
	case b.pending != "":
		return "owner_record_pending"
	case b.failed:
		return "owner_record_failed"
	case !found:
		return "owner_delivery_unknown"
	default:
		return "owner_route_changed"
	}
}

func safeOpenAIRecoveryShadowState(value string) string {
	switch value {
	case "not_consulted", "not_checked", "miss", "unavailable", "invalid", "hit", "changed", "missing", "pending", "ready", "not_ready":
		return value
	default:
		return "unknown"
	}
}

// Only fixed categories/counts cross the request logger boundary. Public error
// codes/messages and persisted logical status remain the existing contract.
func LogOpenAIAgentRecoveryRefusal(ctx context.Context, recovery *OpenAIAgentRecoveryError) {
	d := openAIRecoveryDiagnostics{}
	if recovery != nil && recovery.diagnostic != nil {
		d = *recovery.diagnostic
	}
	ownerObservation, toolObservation := "unknown", "unknown"
	if d.owners.observed {
		ownerObservation = "complete"
		if d.owners.unexamined > 0 {
			ownerObservation = "partial"
		}
	}
	if d.tools.observed {
		toolObservation = "complete"
	}
	logger.FromContext(ctx).Warn("openai.agent_recovery_refused",
		zap.String("subreason", safeOpenAIRecoverySubreason(d.subreason)),
		zap.String("owner_observation", ownerObservation), zap.String("tool_observation", toolObservation),
		zap.Int("owner_total", d.owners.total), zap.Int("owner_completed", d.owners.completed),
		zap.Int("owner_pending", d.owners.pending), zap.Int("owner_failed", d.owners.failed),
		zap.Int("owner_conflicted", d.owners.conflicted), zap.Int("owner_unknown", d.owners.unknown), zap.Int("owner_unexamined", d.owners.unexamined),
		zap.Int("tool_calls", d.tools.calls), zap.Int("tool_results", d.tools.results), zap.Int("tool_pairs", d.tools.pairs),
		zap.Int("tool_unpaired", d.tools.unpaired), zap.Int("tool_duplicate", d.tools.duplicate), zap.Int("tool_incomplete", d.tools.incomplete), zap.Int("tool_invalid", d.tools.invalid),
		zap.String("shadow_lookup", safeOpenAIRecoveryShadowState(d.shadow.lookup)), zap.String("shadow_epoch", safeOpenAIRecoveryShadowState(d.shadow.epoch)),
	)
}

// RecoveryControlsDiagnostic returns only fixed comparison categories and
// presence flags. Field contents and fingerprints are never exposed.
func (e *OpenAIAgentRecoveryError) RecoveryControlsDiagnostic() *OpsRecoveryControls {
	if e == nil || e.diagnostic == nil || e.diagnostic.controls == nil {
		return nil
	}
	copy := *e.diagnostic.controls
	return &copy
}
