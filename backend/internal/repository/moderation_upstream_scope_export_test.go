//go:build integration

package repository

import (
	"database/sql"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent"
)

// Test-only bridge keeps real HTTP handlers out of repository's import graph.
func ModerationScopeTestClient(t *testing.T) (*ent.Client, *sql.DB) {
	return testEntClient(t), integrationDB
}
