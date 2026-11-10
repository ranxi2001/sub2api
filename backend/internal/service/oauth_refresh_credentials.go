package service

import (
	"context"
	"errors"
	"time"
)

// OAuthRefreshSuccessRepository publishes token rotations without replacing
// configuration edited while the provider request was in flight. Implementors
// compare the attempted token identity and merge only changed credential keys
// atomically with scheduler invalidation. Grok retains its stricter success CAS.
type OAuthRefreshSuccessRepository interface {
	MergeOAuthRefreshCredentialsIfTokensUnchanged(context.Context, *Account, map[string]any) (bool, error)
}

func persistOAuthRefreshedCredentials(ctx context.Context, repo AccountRepository, account, attempted *Account, credentials map[string]any) (*Account, bool, error) {
	if attempted == nil || repo == nil || attempted.AuthConnectionID != "" || attempted.IsCredentialShadow() || attempted.IsGrokOAuth() {
		err := persistAccountCredentials(ctx, repo, account, credentials)
		return account, err == nil, err
	}
	merger, ok := repo.(OAuthRefreshSuccessRepository)
	if !ok {
		// Compatibility repositories keep the existing persistence contract.
		err := persistAccountCredentials(ctx, repo, account, credentials)
		return account, err == nil, err
	}
	applied, err := merger.MergeOAuthRefreshCredentialsIfTokensUnchanged(ctx, attempted, credentials)
	if err != nil {
		return nil, false, err
	}
	// Read the durable row even after a CAS miss: another refresher or admin may
	// have replaced the identity. Never publish the pre-refresh account snapshot
	// over its latest model mapping, status, or native degradation settings.
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	current, err := repo.GetByID(readCtx, attempted.ID)
	if err == nil && current == nil {
		err = errors.New("account not found after OAuth credential persistence")
	}
	if err != nil {
		return nil, applied, &providerCycleContainmentRefreshError{err: err}
	}
	return current, applied, nil
}
