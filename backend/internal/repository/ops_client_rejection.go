package repository

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// alias is supplied only by repository code, never by a request parameter.
func opsExplicitRequestRejectionSQL(alias, statuses string, phrases []string) string {
	p := alias + "."
	needles := make([]string, 0, len(phrases))
	for _, needle := range phrases {
		needles = append(needles, "'%"+strings.ReplaceAll(needle, "'", "''")+"%'")
	}
	return `(COALESCE(` + p + `status_code, 0) >= 400
 AND COALESCE(NULLIF(` + p + `upstream_status_code, 0), ` + p + `status_code, 0) IN (` + statuses + `)
 AND lower(CONCAT_WS(' ', ` + p + `error_message, ` + p + `upstream_error_message)) LIKE ANY(ARRAY[` + strings.Join(needles, ",") + `]))`
}

func opsContextLimitRejectionSQL(alias string) string {
	p := alias + "."
	standard := opsExplicitRequestRejectionSQL(alias, "400, 413, 422", service.OpsContextLimitRejectionNeedles)
	// Match the literal code (LIKE would treat underscores as wildcards).
	// Do not inspect earlier attempts: a later provider failure must stay visible.
	wrapped := `(COALESCE(` + p + `status_code, 0) >= 400
 AND COALESCE(` + p + `error_phase, '') <> 'account_auth'
 AND COALESCE(NULLIF(` + p + `upstream_status_code, 0), ` + p + `status_code, 0) = 502
 AND strpos(lower(CONCAT_WS(' ', ` + p + `error_message, ` + p + `upstream_error_message, ` + p + `upstream_error_detail)), 'context_length_exceeded') > 0)`
	// New normalized 400s retain raw HTTP200 SSE or 5xx evidence.
	normalized := `(` + p + `status_code = 400
 AND strpos(lower(CONCAT_WS(' ', ` + p + `error_message, ` + p + `upstream_error_message)), 'context_length_exceeded') > 0)`
	return `(` + standard + ` OR ` + wrapped + ` OR ` + normalized + `)`
}

func opsClientRejectionSQL(alias string) string {
	p := alias + "."
	model := opsExplicitRequestRejectionSQL(alias, "400, 404", service.OpsModelCapabilityRejectionNeedles)
	contextLimit := opsContextLimitRejectionSQL(alias)
	return `COALESCE((` + model + ` OR ` + contextLimit + ` OR (
 ((COALESCE(` + p + `is_business_limited, FALSE)
   AND (COALESCE(` + p + `error_owner, '') = 'client'
     OR (` + p + `error_owner = 'platform' AND ` + p + `error_phase = 'routing'
       AND ` + p + `error_type = 'model_not_found' AND ` + p + `account_id IS NULL)))
  OR (` + p + `error_owner = 'client' AND ` + p + `error_phase = 'request'
      AND ` + p + `error_type IN ('invalid_request_error', 'model_not_found', 'billing_error')
      AND ` + p + `status_code BETWEEN 400 AND 499))
 AND COALESCE(` + p + `upstream_status_code, 0) = 0
 AND COALESCE(` + p + `error_source, '') <> 'upstream_http'
 AND CASE WHEN jsonb_typeof(` + p + `upstream_errors) = 'array'
   THEN jsonb_array_length(` + p + `upstream_errors) = 0 ELSE TRUE END)), FALSE)`
}

func opsBusinessLimitedSQL(alias string) string {
	p := alias + "."
	// Older routing logs marked exhausted account pools as business limits.
	// Capacity is a platform failure; keep local model rejections excluded.
	capacity := `(COALESCE(` + p + `error_owner, '') = 'platform'
 AND COALESCE(` + p + `error_phase, '') = 'routing'
 AND COALESCE(` + p + `error_type, '') <> 'model_not_found'
 AND COALESCE(` + p + `status_code, 0) IN (429, 503))`
	return `((COALESCE(` + p + `is_business_limited, FALSE) AND NOT ` + capacity + `) OR ` + opsClientRejectionSQL(alias) + `)`
}

// Read-time compatibility: old logs keep their original storage, but list and
// detail views use the same responsibility as the SLA exclusion predicate.
func opsEffectiveOwnerSQL(alias string) string {
	return `(CASE WHEN ` + opsClientRejectionSQL(alias) + ` THEN 'client' ELSE COALESCE(` + alias + `.error_owner, '') END)`
}

func opsEffectivePhaseSQL(alias string) string {
	return `(CASE WHEN ` + opsClientRejectionSQL(alias) + ` THEN 'request' ELSE ` + alias + `.error_phase END)`
}

func opsEffectiveTypeSQL(alias string) string {
	model := opsExplicitRequestRejectionSQL(alias, "400, 404", service.OpsModelCapabilityRejectionNeedles)
	contextLimit := opsContextLimitRejectionSQL(alias)
	return `(CASE WHEN ` + model + ` THEN 'model_not_found' WHEN ` + contextLimit + ` THEN 'context_limit' ELSE ` + alias + `.error_type END)`
}

// Forwarding and serving Pods can persist the same failure. Count one final
// outcome per scoped request, while retaining recovered provider-attempt rows
// separately for upstream diagnostics. Missing IDs remain separate events.
func opsMetricErrorRowsSQL(where string) string {
	key := `COALESCE(api_key_id, 0), COALESCE(group_id, 0),
 COALESCE(NULLIF(request_id, ''), 'error:' || id::text), (COALESCE(status_code, 0) >= 400 OR error_type = 'transport_error')`
	return `(SELECT DISTINCT ON (` + key + `) current_error.*,
 ` + opsBusinessLimitedSQL("current_error") + ` AS effective_business_limited
 FROM ops_error_logs current_error
 ` + where + `
 ORDER BY ` + key + `, created_at DESC, id DESC) metric_errors`
}
