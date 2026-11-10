//go:build unit || integration

package repository

import (
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type issue765ImageCase struct {
	name    string
	account service.Account
	request service.OpenAIImagesRequest
	channel string
	allowed bool
}

func issue765ImageCases() []issue765ImageCase {
	const model = "nano-banana-2"
	var cases []issue765ImageCase
	add := func(name string, extra map[string]any, allowed bool) {
		cases = append(cases, issue765ImageCase{
			name: name, allowed: allowed,
			account: service.Account{
				ID: 765, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Status: service.StatusActive, Schedulable: true, Concurrency: 2,
				Credentials: map[string]any{
					"api_key": "synthetic-ci-key", "access_token": "synthetic-access-token",
					"refresh_token": "synthetic-refresh-token", "email": "synthetic@example.invalid",
					"model_mapping": map[string]any{model: model},
				},
				Extra: extra,
			},
			request: service.OpenAIImagesRequest{Endpoint: "/v1/images/generations", Model: model},
		})
	}
	add("exact", map[string]any{service.ExtraImageModelsKey: []string{model}}, true)
	add("prefix", map[string]any{service.ExtraImageModelPrefixesKey: []any{"nano-banana-"}}, true)
	add("unauthorized", nil, false)
	add("unmatched", map[string]any{service.ExtraImageModelsKey: []string{"another-model"}}, false)
	add("exact_is_not_prefix", map[string]any{service.ExtraImageModelsKey: []string{"nano-banana-"}}, false)
	add("empty_prefix", map[string]any{service.ExtraImageModelPrefixesKey: []any{"", " ", 1}}, false)
	add("wrong_type", map[string]any{service.ExtraImageModelPrefixesKey: "nano-banana-"}, false)
	add("case_sensitive", map[string]any{service.ExtraImageModelPrefixesKey: []string{"NANO-"}}, false)
	for _, platform := range []string{service.PlatformGemini, service.PlatformAnthropic} {
		add("platform_"+platform, map[string]any{service.ExtraImageModelsKey: []string{model}}, false)
		cases[len(cases)-1].account.Platform = platform
	}
	for _, typ := range []string{service.AccountTypeOAuth, service.AccountTypeSetupToken} {
		add("type_"+typ, map[string]any{service.ExtraImageModelsKey: []string{model}}, false)
		cases[len(cases)-1].account.Type = typ
	}
	for _, endpoint := range []string{"/v1/images/edits", "/v1/responses", "/v1/chat/completions"} {
		add("endpoint_"+endpoint, map[string]any{service.ExtraImageModelsKey: []string{model}}, false)
		cases[len(cases)-1].request.Endpoint = endpoint
	}
	for _, allowed := range []bool{true, false} {
		name := "mapping_complete"
		grants := []string{"public-image", "provider-image", model}
		if !allowed {
			name, grants = "mapping_target_unauthorized", grants[:2]
		}
		add(name, map[string]any{service.ExtraImageModelsKey: grants}, allowed)
		tc := &cases[len(cases)-1]
		tc.request.Model, tc.channel = "public-image", "provider-image"
		tc.account.Credentials["model_mapping"] = map[string]any{"provider-image": model}
	}
	return cases
}

func issue765CheckImageAuthorization(t *testing.T, tc issue765ImageCase, account *service.Account, payload string) {
	t.Helper()
	require.NotNil(t, account)
	err := tc.request.ValidateAccountModels(account, tc.channel)
	if tc.allowed {
		var modelErr *service.OpenAIImagesModelError
		if payload == "metadata" && errors.As(err, &modelErr) && modelErr.Model == tc.request.Model {
			t.Errorf("ISSUE765_AUTHORIZATION_LOST payload=metadata grant=%s model=%s", tc.name, modelErr.Model)
		}
		require.NoError(t, err, "payload=%s grant=%s", payload, tc.name)
	} else {
		var modelErr *service.OpenAIImagesModelError
		require.ErrorAs(t, err, &modelErr, "payload=%s case=%s", payload, tc.name)
	}
	if payload == "metadata" {
		for _, key := range []string{"access_token", "refresh_token", "email"} {
			require.NotContains(t, account.Credentials, key)
		}
		require.NotContains(t, account.Extra, "unrelated_private_payload")
	} else {
		require.Equal(t, "synthetic-access-token", account.GetCredential("access_token"))
	}
}

func TestIssue765ImageAuthorizationMarshal(t *testing.T) {
	for _, tc := range issue765ImageCases() {
		t.Run(tc.name, func(t *testing.T) {
			if tc.account.Extra == nil {
				tc.account.Extra = map[string]any{}
			}
			tc.account.Extra["unrelated_private_payload"] = "synthetic-private-data"
			full, metadata, err := marshalSchedulerCacheAccount(tc.account)
			require.NoError(t, err)
			for _, payload := range []struct {
				name string
				data []byte
			}{{"full", full}, {"metadata", metadata}} {
				decoded, err := decodeCachedAccount(payload.data)
				require.NoError(t, err)
				issue765CheckImageAuthorization(t, tc, decoded, payload.name)
			}
		})
	}
}
