//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

const pr899ServiceAccountJSON = `{"type":"service_account","project_id":"fixture-project","client_email":"fixture@example.invalid","private_key":"synthetic-private-key"}`

type pr899AuthRepo struct {
	*accountRepoStubForBulkUpdate
	modelWrites int
}

func (r *pr899AuthRepo) SetGroupAllowedModels(context.Context, int64, map[int64][]string) error {
	r.modelWrites++
	return nil
}

func TestPR899VertexAuthRejectsBeforeAnyAccountOrGroupWrite(t *testing.T) {
	cases := []struct {
		name     string
		stored   map[string]any
		incoming map[string]any
	}{
		{"inherited_api_key", map[string]any{"auth_mode": "apikey"}, nil},
		{"inherited_api_key_empty_update", map[string]any{"auth_mode": "apikey"}, map[string]any{}},
		{"inherited_api_key_redacted_update", map[string]any{"auth_mode": "apikey"}, map[string]any{"location": "global"}},
		{"inherited_api_key_even_with_json", map[string]any{"auth_mode": "apikey", "service_account_json": pr899ServiceAccountJSON}, nil},
		{"explicit_api_key_over_json", map[string]any{"service_account_json": pr899ServiceAccountJSON}, map[string]any{"auth_mode": "apikey", "api_key": "fixture-new-key"}},
		{"explicit_trimmed_api_key", map[string]any{"service_account_json": pr899ServiceAccountJSON}, map[string]any{"auth_mode": " apikey "}},
	}
	for index, mode := range []any{nil, false, 1.0, []any{"apikey"}, map[string]any{"mode": "apikey"}, "", " ", "APIKEY", "oauth", "unknown"} {
		cases = append(cases, struct {
			name     string
			stored   map[string]any
			incoming map[string]any
		}{fmt.Sprintf("malformed_explicit_%d", index), map[string]any{"auth_mode": "apikey"}, map[string]any{"auth_mode": mode}})
		cases = append(cases, struct {
			name     string
			stored   map[string]any
			incoming map[string]any
		}{fmt.Sprintf("malformed_inherited_%d", index), map[string]any{"auth_mode": mode}, map[string]any{"location": "global"}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newPlatformEditService()
			recorder := &pr899AuthRepo{accountRepoStubForBulkUpdate: repo}
			svc.accountRepo = recorder
			source := repo.getByIDAccounts[7]
			source.Platform, source.Type = PlatformGemini, AccountTypeServiceAccount
			source.Credentials = tc.stored
			source.GroupIDs = []int64{3}
			groups := []int64{2, 3}
			_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{
				Platform: PlatformAnthropic, Credentials: tc.incoming,
				GroupIDs: &groups, GroupAllowedModels: map[int64][]string{2: {"fixture-model"}},
				SkipMixedChannelCheck: true,
			})
			require.Error(t, err, "unsupported Vertex authentication must fail before persistence")
			require.Empty(t, repo.updatedAccounts, "no account writes")
			require.Empty(t, repo.bindGroupsCalls, "no group binding writes")
			require.Zero(t, recorder.modelWrites, "no group model writes")
			require.Equal(t, PlatformGemini, source.Platform)
			require.Equal(t, tc.stored, source.Credentials)
			require.Equal(t, []int64{3}, source.GroupIDs)
		})
	}
}

func TestPR899VertexAPIKeySamePlatformPreservesAuthentication(t *testing.T) {
	for _, platform := range []string{"", PlatformGemini} {
		for _, replaceKey := range []bool{false, true} {
			t.Run(fmt.Sprintf("platform=%s/replace=%t", platform, replaceKey), func(t *testing.T) {
				svc, repo := newPlatformEditService()
				source := repo.getByIDAccounts[7]
				source.Platform, source.Type = PlatformGemini, AccountTypeServiceAccount
				source.Credentials = map[string]any{"auth_mode": "apikey", "api_key": "fixture-key"}
				incoming := map[string]any{"location": "global", "tier_id": "vertex"}
				wantKey := "fixture-key"
				if replaceKey {
					wantKey = "fixture-replacement-key"
					incoming["api_key"] = wantKey
				}
				_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Platform: platform, Credentials: incoming})
				require.NoError(t, err)
				require.Len(t, repo.updatedAccounts, 1)
				updated := repo.updatedAccounts[0]
				require.True(t, updated.IsVertexAPIKey())
				require.Equal(t, "apikey", updated.Credentials["auth_mode"])
				require.Equal(t, wantKey, updated.Credentials["api_key"])
				require.NotContains(t, incoming, "auth_mode", "do not mutate the caller's credential map")
			})
		}
	}
}

func TestPR899VertexServiceAccountJSONRemainsPortable(t *testing.T) {
	for _, sourcePlatform := range []string{PlatformGemini, PlatformAnthropic} {
		for _, explicitMode := range []bool{false, true} {
			for _, explicitCredentials := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/mode=%t/credentials=%t", sourcePlatform, explicitMode, explicitCredentials), func(t *testing.T) {
					svc, repo := newPlatformEditService()
					source := repo.getByIDAccounts[7]
					source.Platform, source.Type = sourcePlatform, AccountTypeServiceAccount
					source.Credentials = map[string]any{"service_account_json": pr899ServiceAccountJSON, "location": "us-central1"}
					if explicitMode {
						source.Credentials["auth_mode"] = "service_account"
					}
					target := PlatformAnthropic
					if sourcePlatform == PlatformAnthropic {
						target = PlatformGemini
					}
					input := &UpdateAccountInput{Platform: target}
					if explicitCredentials {
						input.Credentials = map[string]any{"location": "global"}
					}
					_, err := svc.UpdateAccount(context.Background(), 7, input)
					require.NoError(t, err)
					require.Len(t, repo.updatedAccounts, 1)
					updated := repo.updatedAccounts[0]
					require.Equal(t, target, updated.Platform)
					require.Equal(t, pr899ServiceAccountJSON, updated.Credentials["service_account_json"])
					_, err = parseVertexServiceAccountKey(updated)
					require.NoError(t, err, "JSON authentication remains usable without an external token exchange")
					require.False(t, updated.IsVertexAPIKey())
				})
			}
		}
	}
}
