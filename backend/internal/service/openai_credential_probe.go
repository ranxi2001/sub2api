package service

import (
	"context"
	"errors"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// FetchOpenAIModelsListFresh verifies an OAuth parent account against the native
// Codex endpoint. It does not use model-catalog caches or merge Excel/BPS access:
// a separate inference grant cannot establish this parent token's health.
func (s *OpenAIGatewayService) FetchOpenAIModelsListFresh(ctx context.Context, account *Account) (*OpenAIModelsResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || account == nil || !account.IsOpenAIOAuth() || account.IsCredentialShadow() || account.IsOpenAIPersonalAccessToken() || account.IsOpenAIAgentIdentity() {
		return nil, infraerrors.BadRequest("OPENAI_CREDENTIAL_PROBE_ACCOUNT_INVALID", "credential inspection requires an OpenAI OAuth parent account")
	}
	clientVersion := CodexCanonicalClientVersion()
	if s.settingService != nil {
		clientVersion = s.settingService.GetOpenAICodexClientVersion(ctx)
	}
	response, err := s.fetchNativeCodexModelsManifestWithCache(ctx, account, clientVersion, "", false)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if response == nil || response.NotModified {
		return nil, invalidOpenAIModelsList(errors.New("credential inspection requires a current model response"))
	}
	body, err := standardOpenAIModelsBody(response.Body, true)
	if err != nil {
		return nil, invalidOpenAIModelsList(err)
	}
	return &OpenAIModelsResponse{Body: body, ETag: codexModelsManifestBodyETag(body)}, nil
}
