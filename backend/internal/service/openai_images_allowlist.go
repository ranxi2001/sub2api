package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	ExtraImageModelsKey        = "extra_image_models"
	ExtraImageModelPrefixesKey = "extra_image_model_prefixes"
)

var ErrImageModelAuthorizationUnavailable = errors.New("image model authorization is unavailable")

// AllowsExtraImageModel grants only the API-key Images generations path. It
// deliberately does not participate in native model classification or pricing.
func (a *Account) AllowsExtraImageModel(model string) bool {
	if a == nil || !a.IsOpenAI() || a.Type != AccountTypeAPIKey {
		return false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, key := range []string{ExtraImageModelsKey, ExtraImageModelPrefixesKey} {
		match := func(entry string) bool {
			entry = strings.TrimSpace(entry)
			return entry != "" && (model == entry || (key == ExtraImageModelPrefixesKey && strings.HasPrefix(model, entry)))
		}
		switch entries := a.Extra[key].(type) {
		case []string:
			for _, entry := range entries {
				if match(entry) {
					return true
				}
			}
		case []any:
			for _, entry := range entries {
				if value, ok := entry.(string); ok && match(value) {
					return true
				}
			}
		}
	}
	return false
}

// OpenAIImagesModelError distinguishes local model rejection from upstream
// failure, including a mapping rejected after account selection.
type OpenAIImagesModelError struct{ Model string }

func (e *OpenAIImagesModelError) Error() string {
	if e.Model == "" {
		return "images endpoint requires an image model"
	}
	return fmt.Sprintf("images endpoint requires an image model, got %q", e.Model)
}

func (r *OpenAIImagesRequest) needsExtraImageAuthorization(channelModel string) bool {
	return validateCompatibleImagesModel(r.Model) != nil ||
		(strings.TrimSpace(channelModel) != "" && validateCompatibleImagesModel(channelModel) != nil)
}

// ValidateAccountModels checks the complete chain against one account. Existing
// composite aliases for built-in models retain their behavior; introducing a
// custom model also requires explicit approval of the original public alias.
func (r *OpenAIImagesRequest) ValidateAccountModels(account *Account, channelModel string) error {
	if account == nil {
		return &OpenAIImagesModelError{Model: r.Model}
	}
	requestModel := firstNonEmptyString(strings.TrimSpace(channelModel), r.Model)
	upstreamModel := account.GetMappedModel(requestModel)
	models := []string{r.Model, requestModel, upstreamModel}
	if r.needsExtraImageAuthorization(channelModel) || validateCompatibleImagesModel(upstreamModel) != nil {
		models = append(models, r.clientModel)
	}
	for _, model := range models {
		if model == "" || validateCompatibleImagesModel(model) == nil {
			continue
		}
		if r.Endpoint == openAIImagesGenerationsEndpoint && account.AllowsExtraImageModel(model) {
			continue
		}
		return &OpenAIImagesModelError{Model: model}
	}
	return nil
}

// ValidateImagesRouting runs before acquiring slots or writing keepalive bytes.
// Only custom model names need this additional repository read. Authorization
// includes active but currently unavailable accounts: lack of capacity must not
// turn an explicitly authorized request into a model-validation error.
func (s *OpenAIGatewayService) ValidateImagesRouting(c *gin.Context, r *OpenAIImagesRequest, channelModel string) error {
	if !r.needsExtraImageAuthorization(channelModel) {
		return nil
	}
	invalidModel := r.Model
	if validateCompatibleImagesModel(invalidModel) == nil {
		invalidModel = channelModel
	}
	rejected := &OpenAIImagesModelError{Model: invalidModel}
	if r.Endpoint != openAIImagesGenerationsEndpoint || s.accountRepo == nil {
		return rejected
	}
	if !r.authorizationLoaded {
		value, _ := c.Get("api_key")
		apiKey, ok := value.(*APIKey)
		if !ok || apiKey == nil {
			return rejected
		}
		ctx := c.Request.Context()
		var accounts []Account
		var err error
		simpleMode := simpleModeAccountScope(ctx, s.cfg)
		if apiKey.GroupID != nil && !simpleMode {
			accounts, err = s.accountRepo.ListByGroup(ctx, *apiKey.GroupID)
		} else {
			accounts, err = s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
			if !simpleMode {
				ungrouped := make([]Account, 0, len(accounts))
				for _, account := range accounts {
					if len(account.GroupIDs) == 0 && len(account.Groups) == 0 {
						ungrouped = append(ungrouped, account)
					}
				}
				accounts = ungrouped
			}
		}
		if err != nil {
			return ErrImageModelAuthorizationUnavailable
		}
		r.authorizationAccounts, r.authorizationLoaded = filterAPIKeyGroupAccounts(ctx, accounts), true
	}
	for i := range r.authorizationAccounts {
		account := &r.authorizationAccounts[i]
		if !accountAllowedByAPIKeyGroup(c.Request.Context(), account) {
			continue
		}
		if account.IsOpenAI() && account.Type == AccountTypeAPIKey && r.ValidateAccountModels(account, channelModel) == nil {
			return nil
		}
	}
	return rejected
}

type imagesAuthorizationContextKey struct{}
type imagesAuthorization struct {
	request      *OpenAIImagesRequest
	channelModel string
}

// WithModelAuthorization keeps sticky, fallback and fresh-account selection
// subject to the same per-account grant as ingress validation.
func (r *OpenAIImagesRequest) WithModelAuthorization(ctx context.Context, channelModel string) context.Context {
	if !r.needsExtraImageAuthorization(channelModel) {
		return ctx
	}
	return context.WithValue(ctx, imagesAuthorizationContextKey{}, imagesAuthorization{r, channelModel})
}

func extraImagesAccountRejected(ctx context.Context, account *Account) bool {
	if ctx == nil {
		return false
	}
	policy, ok := ctx.Value(imagesAuthorizationContextKey{}).(imagesAuthorization)
	return ok && (account == nil || account.Type != AccountTypeAPIKey || policy.request.ValidateAccountModels(account, policy.channelModel) != nil)
}
