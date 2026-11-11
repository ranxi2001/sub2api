//go:build vipcreditintegration

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Decode through the public JSON shape so the baseline compiles and fails on
// behavior (missing default/persistence), rather than undefined new Go fields.
func TestVipCreditEmailConfigCompatibility(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	uid := creditUser(t, db, "0")
	repo := NewAutoCreditRepository(db)
	check := func(want bool) *service.AutoCreditState {
		t.Helper()
		state, err := repo.Get(ctx, uid, "all")
		require.NoError(t, err)
		encoded, err := json.Marshal(state.Config)
		require.NoError(t, err)
		var config map[string]any
		require.NoError(t, json.Unmarshal(encoded, &config))
		require.Equal(t, want, config["notify_email"])
		return state
	}
	check(false)
	var patch service.AutoCreditPatch
	require.NoError(t, json.Unmarshal([]byte(`{"version":0,"enabled":true,"notify_email":true,"threshold":"50","amount":"300","cap":"500"}`), &patch))
	_, err := repo.Save(ctx, uid, uid, patch)
	require.NoError(t, err)
	state := check(true)
	_, err = repo.Save(ctx, uid, uid, service.AutoCreditPatch{Version: state.Config.Version, Enabled: boolPtr(false)})
	require.NoError(t, err)
	state = check(true) // Legacy clients omitting the field must preserve it.
	require.NoError(t, json.Unmarshal([]byte(`{"version":2,"notify_email":false,"threshold":null,"amount":null,"cap":null}`), &patch))
	_, err = repo.Save(ctx, uid, uid, patch)
	require.NoError(t, err)
	state = check(false)
	require.Nil(t, state.Config.Amount)
}
