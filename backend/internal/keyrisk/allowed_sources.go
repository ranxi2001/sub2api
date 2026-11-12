package keyrisk

import (
	"context"
	"database/sql"
	"encoding/json"

	ippkg "github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/lib/pq"
)

// Explicit per-key whitelists describe approved multi-exit use. Read them once
// per sweep, outside authentication, and keep blacklist precedence unchanged.
func allowedSourceFindings(ctx context.Context, tx *sql.Tx, findings []Finding) ([]Finding, error) {
	if len(findings) == 0 {
		return findings, nil
	}
	keyIDs := make([]int64, 0, len(findings))
	for _, f := range findings {
		keyIDs = append(keyIDs, f.Evidence.KeyIDs[0])
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,ip_whitelist,ip_blacklist FROM api_keys WHERE id=ANY($1)`, pq.Array(keyIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type acl struct{ allow, deny *ippkg.CompiledIPRules }
	rules := map[int64]acl{}
	for rows.Next() {
		var id int64
		var allowRaw, denyRaw []byte
		if err = rows.Scan(&id, &allowRaw, &denyRaw); err != nil {
			return nil, err
		}
		var allow, deny []string
		if len(allowRaw) > 0 {
			if err = json.Unmarshal(allowRaw, &allow); err != nil {
				return nil, err
			}
		}
		if len(denyRaw) > 0 {
			if err = json.Unmarshal(denyRaw, &deny); err != nil {
				return nil, err
			}
		}
		if len(allow) > 0 {
			rules[id] = acl{ippkg.CompileIPRules(allow), ippkg.CompileIPRules(deny)}
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		rule, configured := rules[f.Evidence.KeyIDs[0]]
		allAllowed := configured
		if configured {
			for _, ip := range f.Evidence.IPs {
				allowed, _ := ippkg.CheckIPRestrictionWithCompiledRules(ip, rule.allow, rule.deny)
				allAllowed = allAllowed && allowed
			}
		}
		if !allAllowed {
			out = append(out, f)
		}
	}
	return out, nil
}
