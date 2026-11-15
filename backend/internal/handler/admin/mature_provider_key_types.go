// Adapted from mizaawa/zayuapi v0.2.219, a71aa9615ccb55a19973908c9f261b5ba25a48c6.
// LGPL v3; upstream authors retain their copyrights. See mature_provider_NOTICE.md.
package admin

import (
	"errors"
	"fmt"
	"net/http"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Provider-owned model API keys are resolved only on the server. The admin UI
// receives key metadata; a complete key is copied straight into account
// credentials and is never returned by these endpoints.

const (
	// providerKeyNamePrefix marks keys that this panel creates upstream.
	providerKeyNamePrefix      = "[Sub2API]"
	providerKeyBindingExtraKey = "provider_binding"
	providerKeyPageSize        = 100
	providerKeyMaxPages        = 50
	providerKeyResponseLimit   = 8 << 20
)

var (
	errProviderKeyMissing           = errors.New("provider API key no longer exists")
	errProviderKeyUnavailable       = errors.New("provider API key is not active")
	errProviderKeySecretUnavailable = errors.New("provider did not return a complete API key")
	errProviderGroupUnavailable     = errors.New("provider group is no longer available")
	errProviderKeyListTooLarge      = errors.New("provider returned too many API keys")
	errProviderStoredLogin          = errors.New("stored provider login is unavailable")
)

// providerAPIError keeps an upstream application message, such as a refused
// group, so the operator can act on it. The message is sanitized and bounded.
type providerAPIError struct {
	status  int
	message string
}

func (e *providerAPIError) Error() string {
	return fmt.Sprintf("provider rejected the request (HTTP %d): %s", e.status, e.message)
}

// providerKeyCreateUncertainError reports a failed creation request whose
// upstream write may nevertheless have been committed.
type providerKeyCreateUncertainError struct{ cause error }

func (e *providerKeyCreateUncertainError) Error() string {
	return "provider API key creation result is unknown: " + e.cause.Error()
}

func (e *providerKeyCreateUncertainError) Unwrap() error { return e.cause }

type providerKeyView struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	GroupID         string   `json:"group_id"`
	GroupName       string   `json:"group_name"`
	GroupPlatform   string   `json:"group_platform,omitempty"`
	GroupMultiplier *float64 `json:"group_multiplier"`
	Status          string   `json:"status"`
	Available       bool     `json:"available"`
	// secret is the model API key. It is unexported and never serialized.
	secret string
}

type providerGroupView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Platform    string   `json:"platform,omitempty"`
	Description string   `json:"description,omitempty"`
	Multiplier  *float64 `json:"multiplier"`
	KeyName     string   `json:"key_name"`
}

type providerCreatedKey struct {
	keyID string
	name  string
}

// providerCreateFailure classifies a failed creation request. Only a definite
// refusal proves that nothing was created; any other failure may follow a
// committed write, so the operator must refresh before trying again.
func providerCreateFailure(err error) error {
	var refused *providerAPIError
	if errors.As(err, &refused) && refused.status < http.StatusInternalServerError {
		return err
	}
	var remote *providerRemoteError
	if errors.As(err, &remote) && remote.status >= http.StatusBadRequest && remote.status < http.StatusInternalServerError {
		return err
	}
	var scope *providerScopeError
	if errors.Is(err, errProviderAuthentication) || errors.Is(err, errProviderTurnstile) || errors.As(err, &scope) {
		return err
	}
	return &providerKeyCreateUncertainError{cause: err}
}

// providerKeyError converts a provider failure into a public API error. It
// never reports 401, which the console treats as its own expired session.
func providerKeyError(err error) error {
	var appErr *infraerrors.ApplicationError
	if errors.As(err, &appErr) {
		return err
	}
	var uncertain *providerKeyCreateUncertainError
	var scope *providerScopeError
	switch {
	case errors.As(err, &uncertain):
		return infraerrors.New(http.StatusBadGateway, "PROVIDER_KEY_CREATE_UNCERTAIN",
			providerFailureMessage(uncertain.cause)+"; refresh the key list to confirm whether the key was created before trying again")
	case errors.Is(err, errProviderKeyMissing):
		return infraerrors.BadRequest("PROVIDER_KEY_NOT_FOUND", "The selected API key no longer exists on the provider; refresh the keys and choose again")
	case errors.Is(err, errProviderKeyUnavailable):
		return infraerrors.BadRequest("PROVIDER_KEY_UNAVAILABLE", "The selected API key is disabled, expired or out of quota; choose another key")
	case errors.Is(err, errProviderKeySecretUnavailable):
		return infraerrors.New(http.StatusBadGateway, "PROVIDER_KEY_SECRET_UNAVAILABLE", "The provider did not return the complete API key")
	case errors.Is(err, errProviderGroupUnavailable):
		return infraerrors.BadRequest("PROVIDER_GROUP_UNAVAILABLE", "This group is no longer available; refresh the groups and choose again")
	case errors.Is(err, errProviderKeyListTooLarge):
		return infraerrors.New(http.StatusBadGateway, "PROVIDER_KEY_LIST_TOO_LARGE", "The provider returned too many API keys")
	case errors.Is(err, errProviderStoredLogin):
		return infraerrors.New(http.StatusBadGateway, "PROVIDER_LOGIN_UNAVAILABLE", "The saved provider login could not be read; edit the provider and sign in again")
	case errors.As(err, &scope):
		denied := infraerrors.New(http.StatusBadGateway, "PROVIDER_SCOPE_DENIED", providerPublicError(err))
		if scope.scope != "" {
			denied = denied.WithMetadata(map[string]string{"scope": scope.scope})
		}
		return denied
	case errors.Is(err, errProviderTokenExpired):
		return infraerrors.New(http.StatusBadGateway, "PROVIDER_TOKEN_EXPIRED", providerPublicError(err))
	case errors.Is(err, errProviderTokenRetired):
		return infraerrors.New(http.StatusBadGateway, "PROVIDER_TOKEN_RETIRED", providerPublicError(err))
	case errors.Is(err, errProviderAuthentication):
		return infraerrors.New(http.StatusBadGateway, "PROVIDER_LOGIN_FAILED", providerPublicError(err))
	default:
		return infraerrors.New(http.StatusBadGateway, "PROVIDER_REMOTE_FAILED", providerFailureMessage(err))
	}
}

func providerFailureMessage(err error) string {
	var refused *providerAPIError
	if errors.As(err, &refused) {
		return "Provider rejected the request; check permissions and selected group"
	}
	return providerPublicError(err)
}
