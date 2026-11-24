package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// UpstreamGroupSyncGuard binds a delayed preview to its source and text. It is
// an optimistic concurrency guard, not an authorization token. Credentials are
// hashed only on the server, including protocol-specific URLs and header config.
type UpstreamGroupSyncGuard struct {
	Source string  `json:"source"`
	Name   string  `json:"name"`
	Notes  *string `json:"notes"`
}

func NewUpstreamGroupSyncGuard(account *Account) (*UpstreamGroupSyncGuard, error) {
	if account == nil {
		return nil, ErrAccountNotFound
	}
	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	data, err := json.Marshal(struct {
		ID          int64
		Platform    string
		Type        string
		Credentials map[string]any
		ProxyID     *int64
		ProxyURL    string
	}{account.ID, account.Platform, account.Type, account.Credentials, account.ProxyID, proxyURL})
	if err != nil {
		return nil, ErrAccountUpdateConflict
	}
	sum := sha256.Sum256(data)
	return &UpstreamGroupSyncGuard{
		Source: hex.EncodeToString(sum[:]), Name: account.Name, Notes: cloneAccountValuePointer(account.Notes),
	}, nil
}
