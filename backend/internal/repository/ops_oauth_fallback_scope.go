package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

// Expressions and aliases come only from repository code. Include accounts
// that are currently paused, errored or soft deleted: their historical requests
// still describe the pool failure. Never infer provider identity from a model.
func openAIOAuthAccountSQL(accountID string) string {
	return `EXISTS (SELECT 1 FROM accounts oauth_account WHERE oauth_account.id = ` + accountID + `
 AND oauth_account.platform = 'openai' AND oauth_account.type = 'oauth')`
}

func opsMetricErrorRowsForFilterSQL(where string, filter *service.OpsDashboardFilter) string {
	rows := opsMetricErrorRowsSQL(where)
	if filter == nil || !filter.OpenAIOAuthOnly {
		return rows
	}
	// Apply attribution AFTER picking the latest final outcome. Otherwise an
	// earlier OAuth copy can survive a later API Key outcome for the same request.
	// A pre-selection 503 without evidence is not an OAuth failure. Pool-wide
	// exhaustion is handled independently by the live OAuth availability check.
	// When the final account is missing, accept only a nonempty attempt chain
	// whose every account is OpenAI OAuth. Mixed/unknown attempts are ambiguous.
	attempts := `CASE WHEN jsonb_typeof(metric_errors.upstream_errors) = 'array'
 THEN metric_errors.upstream_errors ELSE '[]'::jsonb END`
	return `(SELECT metric_errors.* FROM ` + rows + ` WHERE ` + openAIOAuthAccountSQL("metric_errors.account_id") + `
 OR (metric_errors.account_id IS NULL
 AND jsonb_array_length(` + attempts + `) > 0
 AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(` + attempts + `) attempt
   WHERE NOT EXISTS (SELECT 1 FROM accounts oauth_attempt
     WHERE oauth_attempt.id::text = attempt->>'account_id'
       AND oauth_attempt.platform = 'openai' AND oauth_attempt.type = 'oauth')))) oauth_metric_errors`
}
