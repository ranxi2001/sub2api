//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func degradationSelectedBool(v bool) *bool { return &v }

func TestOAuthDegradationSelectedActionValidation(t *testing.T) {
	threshold := 50.0
	c := DefaultOAuthDegradationConfig()
	c.Enabled, c.ThresholdPercent = true, &threshold
	require.NoError(t, c.Validate(), "missing flags retain both existing actions")
	c.LimitConcurrency, c.LimitModels = degradationSelectedBool(false), degradationSelectedBool(false)
	require.Error(t, c.Validate(), "enabled policy must select an action")
	c.Enabled = false
	require.NoError(t, c.Validate(), "disabled drafts may deselect every action")
	c.Enabled, c.LimitGroups = true, true
	require.Error(t, c.Validate(), "group closure requires a nonempty selection")
	for _, ids := range [][]int64{{0}, {-1}, {1, 1}} {
		c.GroupIDs = ids
		require.Error(t, c.Validate())
	}
	c.GroupIDs = []int64{1, 2}
	c.TargetConcurrency, c.Models = 0, []string{"invalid-*"}
	require.NoError(t, c.Validate(), "unselected action parameters must not block a group-only policy")
	c.LimitConcurrency = degradationSelectedBool(true)
	require.Error(t, c.Validate())
	c.LimitConcurrency, c.LimitModels = degradationSelectedBool(false), degradationSelectedBool(true)
	require.Error(t, c.Validate())
}

func TestOAuthDegradationSelectedActionsAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		concurrency, models, groups bool
	}{
		{"concurrency only", true, false, false},
		{"models only", false, true, false},
		{"groups only", false, false, true},
		{"concurrency and models", true, true, false},
		{"concurrency and groups", true, false, true},
		{"models and groups", false, true, true},
		{"all", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := OAuthDegradationState{LimitConcurrency: degradationSelectedBool(tc.concurrency), LimitModels: degradationSelectedBool(tc.models), LimitGroups: tc.groups, GroupIDs: []int64{7}, Models: []string{"gpt-6-luna"}, ConcurrencyCap: 10}
			a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 10, Credentials: map[string]any{}, Extra: map[string]any{OAuthDegradationActionExtraKey: state}}
			require.Equal(t, !tc.models, a.IsModelSupported("gpt-6.1-sol"))
			require.True(t, a.IsModelSupported("gpt-6-luna"))
			if tc.concurrency {
				require.Equal(t, 10, a.LimitOAuthDegradationConcurrency(20))
				require.Equal(t, 10, a.LimitOAuthDegradationConcurrency(0))
			} else {
				require.Equal(t, 20, a.LimitOAuthDegradationConcurrency(20))
				require.Equal(t, 0, a.LimitOAuthDegradationConcurrency(0))
			}
			if tc.groups {
				require.Equal(t, []int64{8}, a.FilterOAuthDegradationGroups([]int64{7, 8}))
			} else {
				require.Equal(t, []int64{7, 8}, a.FilterOAuthDegradationGroups([]int64{7, 8}))
			}
			body := []byte(`{"data":[{"id":"gpt-6-luna"},{"id":"gpt-6.1-sol"}]}`)
			catalog, err := projectAccountModelAliases(body, a, nil, false)
			require.NoError(t, err)
			if tc.models {
				require.NotContains(t, string(catalog), "gpt-6.1-sol")
			} else {
				require.JSONEq(t, string(body), string(catalog), "unselected model restriction must not alter model discovery")
			}
		})
	}
}

func TestOAuthDegradationLegacyActionFlags(t *testing.T) {
	a := &Account{Concurrency: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{}}
	require.NoError(t, json.Unmarshal([]byte(`{"oauth_degradation_action":{"concurrency_cap":10,"models":["gpt-6-luna"]}}`), &a.Extra))
	require.Equal(t, 10, a.LimitOAuthDegradationConcurrency(20))
	require.False(t, a.IsModelSupported("gpt-6.1-sol"))
	require.Equal(t, []int64{7, 8}, a.FilterOAuthDegradationGroups([]int64{7, 8}), "legacy actions did not close groups")
	// New group-only markers do not need model data, unlike legacy model caps.
	require.NoError(t, json.Unmarshal([]byte(`{"oauth_degradation_action":{"limit_concurrency":false,"limit_models":false,"limit_groups":true,"group_ids":[7]}}`), &a.Extra))
	require.True(t, a.IsModelSupported("gpt-6.1-sol"))
	require.Equal(t, 20, a.LimitOAuthDegradationConcurrency(20))
	require.Equal(t, []int64{8}, a.FilterOAuthDegradationGroups([]int64{7, 8}))
}
