//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const extraImageTestModel = "nai-diffusion-4-5-full"

type imageAuthorizationRepo struct {
	AccountRepository
	accounts []Account
	groupID  int64
	reads    int
	err      error
}

func (r *imageAuthorizationRepo) ListByGroup(_ context.Context, groupID int64) ([]Account, error) {
	r.groupID = groupID
	r.reads++
	return r.accounts, r.err
}
func (r *imageAuthorizationRepo) ListByPlatform(_ context.Context, _ string) ([]Account, error) {
	r.reads++
	return r.accounts, r.err
}

func extraImageTestAccount() Account {
	return Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, GroupIDs: []int64{7},
		Extra:       map[string]any{ExtraImageModelPrefixesKey: []any{"nai-diffusion-"}},
		Credentials: map[string]any{"api_key": "test-key", "base_url": "https://images.example/v1"}}
}

func extraImageTestContext(model string) (*gin.Context, *httptest.ResponseRecorder, []byte) {
	gin.SetMode(gin.TestMode)
	body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"a red circle","n":1,"size":"1024x1024","steps":12,"custom_field":{"keep":true}}`, model))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, openAIImagesGenerationsEndpoint, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	groupID := int64(7)
	c.Set("api_key", &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformOpenAI, AllowImageGeneration: true}})
	return c, recorder, body
}

func TestExtraImageModelsExplicitGrants(t *testing.T) {
	for _, tc := range []struct {
		name    string
		extra   map[string]any
		model   string
		allowed bool
	}{
		{"empty", nil, extraImageTestModel, false},
		{"exact", map[string]any{ExtraImageModelsKey: []any{extraImageTestModel}}, extraImageTestModel, true},
		{"exact_is_not_prefix", map[string]any{ExtraImageModelsKey: []string{"nai-diffusion-"}}, extraImageTestModel, false},
		{"prefix", map[string]any{ExtraImageModelPrefixesKey: []string{" nai-diffusion- "}}, extraImageTestModel, true},
		{"empty_prefix", map[string]any{ExtraImageModelPrefixesKey: []any{"", " ", 1}}, "gpt-5.6-sol", false},
		{"wrong_type", map[string]any{ExtraImageModelPrefixesKey: "nai-diffusion-"}, extraImageTestModel, false},
		{"case_sensitive", map[string]any{ExtraImageModelPrefixesKey: []string{"NAI-"}}, extraImageTestModel, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := extraImageTestAccount()
			a.Extra = tc.extra
			require.Equal(t, tc.allowed, a.AllowsExtraImageModel(tc.model))
			for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
				a.Type = typ
				require.False(t, a.AllowsExtraImageModel(tc.model))
			}
		})
	}
	require.False(t, IsGPTImageGenerationModel(extraImageTestModel))
	require.False(t, isOpenAIImageGenerationModel(extraImageTestModel))
}

func TestExtraImageModelsIngressAndScope(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		a := extraImageTestAccount()
		if !allowed {
			a.Extra = nil
		}
		repo := &imageAuthorizationRepo{accounts: []Account{a}}
		svc := &OpenAIGatewayService{accountRepo: repo}
		c, _, body := extraImageTestContext(extraImageTestModel)
		parsed, err := svc.ParseOpenAIImagesRequest(c, body)
		require.Equal(t, int64(7), repo.groupID)
		if !allowed {
			require.ErrorContains(t, err, "images endpoint requires an image model")
			continue
		}
		require.NoError(t, err)
		require.Equal(t, OpenAIImagesCapabilityAPIKey, parsed.RequiredCapability)
		require.NoError(t, svc.ValidateImagesRouting(c, parsed, ""))
		require.Equal(t, 1, repo.reads, "reuse the ingress authorization snapshot within one request")
		// No group must not inherit a different group's account grant.
		a.GroupIDs = []int64{7}
		repo.accounts = []Account{a}
		c.Set("api_key", &APIKey{})
		_, err = svc.ParseOpenAIImagesRequest(c, body)
		require.ErrorContains(t, err, "images endpoint requires an image model")
	}
	for _, model := range []string{"gpt-image-2", "grok-imagine", "gemini-3-pro-image", "banana-pro"} {
		c, _, body := extraImageTestContext(model)
		_, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
		require.NoError(t, err, "built-in models need no account authorization lookup")
	}
}

func TestExtraImageModelsWholeChainAndScheduling(t *testing.T) {
	a := extraImageTestAccount()
	a.Extra[ExtraImageModelsKey] = []string{"my-nai", "provider-image"}
	a.Credentials["model_mapping"] = map[string]any{"provider-image": extraImageTestModel, "my-nai": extraImageTestModel}
	r := &OpenAIImagesRequest{Endpoint: openAIImagesGenerationsEndpoint, Model: "my-nai", clientModel: "my-nai"}
	require.NoError(t, r.ValidateAccountModels(&a, "provider-image"))
	ctx := r.WithModelAuthorization(WithOpenAIImagesEndpoint(context.Background()), "provider-image")
	require.True(t, accountSupportsOpenAICapabilitiesForRequest(ctx, &a, "my-nai", "", OpenAIImagesCapabilityAPIKey))
	for _, missing := range []string{"my-nai", "provider-image", extraImageTestModel} {
		copy := a
		copy.Extra = map[string]any{ExtraImageModelsKey: []string{}}
		for _, model := range []string{"my-nai", "provider-image", extraImageTestModel} {
			if model != missing {
				copy.Extra[ExtraImageModelsKey] = append(copy.Extra[ExtraImageModelsKey].([]string), model)
			}
		}
		require.ErrorContains(t, r.ValidateAccountModels(&copy, "provider-image"), missing)
		require.False(t, accountSupportsOpenAICapabilitiesForRequest(ctx, &copy, "my-nai", "", OpenAIImagesCapabilityAPIKey))
	}
	for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		copy := a
		copy.Type = typ
		require.False(t, accountSupportsOpenAICapabilitiesForRequest(ctx, &copy, "my-nai", "", OpenAIImagesCapabilityAPIKey))
		require.False(t, copy.SupportsOpenAIImageCapability(r.RequiredCapabilityForModel("provider-image")))
	}
	// Two accounts cannot pool their partial grants to authorize one chain.
	aliasOnly := a
	aliasOnly.Extra = map[string]any{ExtraImageModelsKey: []string{"my-nai"}}
	repo := &imageAuthorizationRepo{accounts: []Account{aliasOnly, extraImageTestAccount()}}
	c, _, _ := extraImageTestContext("my-nai")
	err := (&OpenAIGatewayService{accountRepo: repo}).ValidateImagesRouting(c, r, extraImageTestModel)
	require.ErrorContains(t, err, "images endpoint requires an image model")
	// Composite resolution must retain and validate the original public name.
	r.Model = extraImageTestModel
	r.clientModel = "unapproved-public-alias"
	require.ErrorContains(t, r.ValidateAccountModels(&a, ""), "unapproved-public-alias")
}

func TestExtraImageModelsForwardPreservesBodyAndOctetStreamJSON(t *testing.T) {
	for _, mapping := range []string{"none", "channel", "account"} {
		for _, filterHeaders := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/filter=%t", mapping, filterHeaders), func(t *testing.T) {
				a := extraImageTestAccount()
				model, channelModel := extraImageTestModel, ""
				if mapping != "none" {
					model = "my-nai"
					a.Extra[ExtraImageModelsKey] = []string{model}
					if mapping == "channel" {
						channelModel = extraImageTestModel
					} else {
						a.Credentials["model_mapping"] = map[string]any{model: extraImageTestModel}
					}
				}
				c, recorder, body := extraImageTestContext(model)
				parser := &OpenAIGatewayService{accountRepo: &imageAuthorizationRepo{accounts: []Account{a}}}
				parsed, err := parser.ParseOpenAIImagesRequest(c, body)
				require.NoError(t, err)
				response := `{"data":[{"b64_json":"aW1hZ2U=","url":"","revised_prompt":""}]}`
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"binary/octet-stream"}}, Body: io.NopCloser(strings.NewReader(response))}}
				cfg := &config.Config{}
				cfg.Security.ResponseHeaders.Enabled = filterHeaders
				svc := &OpenAIGatewayService{
					cfg: cfg, httpUpstream: upstream,
					responseHeaderFilter: compileResponseHeaderFilter(cfg),
				}
				result, err := svc.ForwardImages(c.Request.Context(), c, &a, body, parsed, channelModel)
				require.NoError(t, err)
				require.Equal(t, extraImageTestModel, result.UpstreamModel)
				require.Equal(t, 1, result.ImageCount)
				require.Equal(t, "/v1/images/generations", upstream.lastReq.URL.Path)
				require.Equal(t, extraImageTestModel, gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, int64(12), gjson.GetBytes(upstream.lastBody, "steps").Int())
				require.True(t, gjson.GetBytes(upstream.lastBody, "custom_field.keep").Bool())
				require.Equal(t, response, recorder.Body.String())
				if filterHeaders {
					require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
				} else {
					require.Equal(t, "binary/octet-stream", recorder.Header().Get("Content-Type"))
				}
			})
		}
	}
}

func TestExtraImageModelsRejectBeforeUpstream(t *testing.T) {
	for _, typ := range []string{AccountTypeAPIKey, AccountTypeOAuth, AccountTypeSetupToken} {
		a := extraImageTestAccount()
		a.Type = typ
		if typ == AccountTypeAPIKey {
			a.Extra = nil
		}
		c, _, body := extraImageTestContext(extraImageTestModel)
		upstream := &httpUpstreamRecorder{}
		svc := &OpenAIGatewayService{httpUpstream: upstream}
		r := &OpenAIImagesRequest{Endpoint: openAIImagesGenerationsEndpoint, Model: extraImageTestModel}
		_, err := svc.ForwardImages(c.Request.Context(), c, &a, body, r, "")
		require.ErrorContains(t, err, "images endpoint requires an image model")
		require.Empty(t, upstream.requests)
	}
	a := extraImageTestAccount()
	r := &OpenAIImagesRequest{Endpoint: openAIImagesEditsEndpoint, Model: extraImageTestModel}
	require.ErrorContains(t, r.ValidateAccountModels(&a, ""), "images endpoint requires an image model")
}

func TestExtraImageModelsAccountTestUsesGenerations(t *testing.T) {
	a := extraImageTestAccount()
	a.Extra[ExtraImageModelsKey] = []string{"my-nai"}
	a.Credentials["model_mapping"] = map[string]any{"my-nai": extraImageTestModel}
	c, recorder := newTestContext()
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"binary/octet-stream"}}, Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aW1hZ2U=","url":""}]}`))}}
	svc := &AccountTestService{cfg: &config.Config{}, httpUpstream: upstream}
	require.NoError(t, svc.testOpenAIAccountConnection(c, &a, "my-nai", "a red circle", ""))
	require.Equal(t, "/v1/images/generations", upstream.lastReq.URL.Path)
	require.Equal(t, extraImageTestModel, gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, recorder.Body.String(), "data:image/png;base64,aW1hZ2U=")
	require.Contains(t, recorder.Body.String(), `"success":true`)
}

func TestExtraImageModelsAuthorizationStorageFailure(t *testing.T) {
	c, _, body := extraImageTestContext(extraImageTestModel)
	repo := &imageAuthorizationRepo{err: errors.New("private database details")}
	_, err := (&OpenAIGatewayService{accountRepo: repo}).ParseOpenAIImagesRequest(c, body)
	require.ErrorIs(t, err, ErrImageModelAuthorizationUnavailable)
	require.NotContains(t, err.Error(), "private database details")
}

func TestExtraImageModelsAsyncUsesSameAuthorization(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		a := extraImageTestAccount()
		if !allowed {
			a.Extra = nil
		}
		c, _, body := extraImageTestContext(extraImageTestModel)
		c.Request.URL.Path = "/v1/images/generations/async"
		svc := &OpenAIGatewayService{accountRepo: &imageAuthorizationRepo{accounts: []Account{a}}}
		parsed, err := svc.ParseOpenAIImagesRequest(c, body)
		if allowed {
			require.NoError(t, err)
			require.Equal(t, openAIImagesGenerationsEndpoint, parsed.Endpoint)
			require.Equal(t, OpenAIImagesCapabilityAPIKey, parsed.RequiredCapability)
		} else {
			require.ErrorContains(t, err, "images endpoint requires an image model")
		}
	}
}

func TestExtraImageModelsAccountTestRejectsPartiallyAllowedAlias(t *testing.T) {
	a := extraImageTestAccount()
	a.Extra = map[string]any{ExtraImageModelsKey: []string{"my-nai"}}
	a.Credentials["model_mapping"] = map[string]any{"my-nai": extraImageTestModel}
	c, recorder := newTestContext()
	upstream := &httpUpstreamRecorder{}
	svc := &AccountTestService{cfg: &config.Config{}, httpUpstream: upstream}
	require.Error(t, svc.testOpenAIAccountConnection(c, &a, "my-nai", "draw", ""))
	require.Contains(t, recorder.Body.String(), "images endpoint requires an image model")
	require.Empty(t, upstream.requests)
}

func TestIssue765ExtraImagesCandidateAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name     string
		platform string
		typ      string
		endpoint string
		extra    map[string]any
		rejected bool
	}{
		{"exact", PlatformOpenAI, AccountTypeAPIKey, openAIImagesGenerationsEndpoint, map[string]any{ExtraImageModelsKey: []any{extraImageTestModel}}, false},
		{"prefix", PlatformOpenAI, AccountTypeAPIKey, openAIImagesGenerationsEndpoint, map[string]any{ExtraImageModelPrefixesKey: []any{"nai-diffusion-"}}, false},
		{"unauthorized", PlatformOpenAI, AccountTypeAPIKey, openAIImagesGenerationsEndpoint, nil, true},
		{"other_platform", PlatformGemini, AccountTypeAPIKey, openAIImagesGenerationsEndpoint, map[string]any{ExtraImageModelsKey: []any{extraImageTestModel}}, true},
		{"oauth", PlatformOpenAI, AccountTypeOAuth, openAIImagesGenerationsEndpoint, map[string]any{ExtraImageModelsKey: []any{extraImageTestModel}}, true},
		{"setup_token", PlatformOpenAI, AccountTypeSetupToken, openAIImagesGenerationsEndpoint, map[string]any{ExtraImageModelsKey: []any{extraImageTestModel}}, true},
		{"edits", PlatformOpenAI, AccountTypeAPIKey, openAIImagesEditsEndpoint, map[string]any{ExtraImageModelsKey: []any{extraImageTestModel}}, true},
		{"responses", PlatformOpenAI, AccountTypeAPIKey, "/v1/responses", map[string]any{ExtraImageModelsKey: []any{extraImageTestModel}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := extraImageTestAccount()
			a.Platform, a.Type, a.Extra = tc.platform, tc.typ, tc.extra
			r := &OpenAIImagesRequest{Endpoint: tc.endpoint, Model: extraImageTestModel}
			ctx := r.WithModelAuthorization(context.Background(), "")
			require.Equal(t, tc.rejected, extraImagesAccountRejected(ctx, &a))
			require.True(t, extraImagesAccountRejected(ctx, nil))
		})
	}
}
